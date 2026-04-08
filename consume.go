package phpserialize

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
)

// The internal consume functions work as the parser/lexer when reading
// individual items off the serialized stream.

// consumeStringUntilByte will return a string that includes all characters
// after the given offset, but only up until (and not including) a found byte.
//
// This function will only work with a plain, non-encoded series of bytes. It
// should not be used to capture anything other that ASCII data that is
// terminated by a single byte.
func consumeStringUntilByte(data []byte, lookingFor byte, offset int) (s string, newOffset int) {
	newOffset = findByte(data, lookingFor, offset)
	if newOffset < 0 {
		return "", -1
	}

	s = string(data[offset:newOffset])
	return
}

func consumeInt(data []byte, offset int) (int64, int, error) {
	if !checkType(data, 'i', offset) {
		return 0, -1, errors.New("not an integer")
	}

	alphaNumber, newOffset := consumeStringUntilByte(data, ';', offset+2)
	i, err := strconv.Atoi(alphaNumber)
	if err != nil {
		return 0, -1, err
	}

	// The +1 is to skip over the final ';'
	return int64(i), newOffset + 1, nil
}

func consumeFloat(data []byte, offset int) (float64, int, error) {
	if !checkType(data, 'd', offset) {
		return 0, -1, errors.New("not a float")
	}

	alphaNumber, newOffset := consumeStringUntilByte(data, ';', offset+2)
	v, err := strconv.ParseFloat(alphaNumber, 64)
	if err != nil {
		return 0, -1, err
	}

	return v, newOffset + 1, nil
}

func consumeString(data []byte, offset int) (string, int, error) {
	if !checkType(data, 's', offset) {
		return "", -1, errors.New("not a string")
	}

	return consumeStringRealPart(data, offset+2)
}

// consumeIntPart will consume an integer followed by and including a colon.
// This is used in many places to describe the number of elements or an upcoming
// length.
func consumeIntPart(data []byte, offset int) (int, int, error) {
	rawValue, newOffset := consumeStringUntilByte(data, ':', offset)
	value, err := strconv.Atoi(rawValue)
	if err != nil {
		return 0, -1, err
	}

	// The +1 is to skip over the ':'
	return value, newOffset + 1, nil
}

func consumeStringRealPart(data []byte, offset int) (string, int, error) {
	length, newOffset, err := consumeIntPart(data, offset)
	if err != nil {
		return "", -1, err
	}

	// Skip over the '"' at the start of the string. I'm not sure why they
	// decided to wrap the string in double quotes since it's totally
	// redundant.
	offset = newOffset + 1

	s := DecodePHPString(data[offset : length+offset])

	// The +2 is to skip over the final '";'
	return s, offset + length + 2, nil
}

func consumeNil(data []byte, offset int) (interface{}, int, error) {
	if !checkType(data, 'N', offset) {
		return nil, -1, errors.New("not null")
	}

	return nil, offset + 2, nil
}

func consumeBool(data []byte, offset int) (bool, int, error) {
	if !checkType(data, 'b', offset) {
		return false, -1, errors.New("not a boolean")
	}

	return data[offset+2] == '1', offset + 4, nil
}

func consumeObjectAsMap(data []byte, offset int) (
	map[interface{}]interface{}, int, error) {
	result := map[interface{}]interface{}{}

	// Read the class name. The class name follows the same format as a
	// string. We could just ignore the length and hope that no class name
	// ever had a non-ascii characters in it, but this is safer - and
	// probably easier.
	_, offset, err := consumeStringRealPart(data, offset+2)
	if err != nil {
		return nil, -1, err
	}

	// Read the number of elements in the object.
	length, offset, err := consumeIntPart(data, offset)
	if err != nil {
		return nil, -1, err
	}

	// Skip over the '{'
	offset++

	// Read the elements
	for i := 0; i < length; i++ {
		var key string
		var value interface{}

		// The key should always be a string. I am not completely sure
		// about this.
		key, offset, err = consumeString(data, offset)
		if err != nil {
			return nil, -1, err
		}

		// If the next item is an object we can't simply consume it,
		// rather we send the reflect.Value back through consumeObject
		// so the recursion can be handled correctly.
		if data[offset] == 'O' {
			var subMap interface{}

			subMap, offset, err = consumeObjectAsMap(data, offset)
			if err != nil {
				return nil, -1, err
			}

			result[key] = subMap
		} else {
			value, offset, err = consumeNext(data, offset)
			if err != nil {
				return nil, -1, err
			}

			result[key] = value
		}
	}

	// The +1 is for the final '}'
	return result, offset + 1, nil
}

func setField(structFieldValue reflect.Value, value interface{}) error {
	if !structFieldValue.IsValid() {
		return nil
	}

	val := reflect.ValueOf(value)
	if !val.IsValid() {
		// structFieldValue will be set to default.
		return nil
	}

	switch structFieldValue.Type().Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if val.CanInt() {
			structFieldValue.SetInt(val.Int())
		} else {
			intVal, err := strconv.ParseInt(fmt.Sprintf("%v", val.Interface()), 10, 64)
			if err != nil {
				return err
			}
			structFieldValue.SetInt(intVal)
		}

	case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if val.CanUint() {
			structFieldValue.SetUint(val.Uint())
		} else {
			uintVal, err := strconv.ParseUint(fmt.Sprintf("%v", val.Interface()), 10, 64)
			if err != nil {
				return err
			}
			structFieldValue.SetUint(uintVal)
		}

	case reflect.Float32, reflect.Float64:
		if val.CanFloat() {
			structFieldValue.SetFloat(val.Float())
		} else {
			floatVal, err := strconv.ParseFloat(fmt.Sprintf("%v", val.Interface()), 64)
			if err != nil {
				return err
			}
			structFieldValue.SetFloat(floatVal)
		}

	case reflect.Struct:
		m := val.Interface().(map[interface{}]interface{})
		return fillStruct(structFieldValue, m)

	case reflect.Slice:
		l := val.Len()
		if l == 0 {
			break
		}

		arrayOfObjects := reflect.MakeSlice(structFieldValue.Type(), l, l)

		for i := 0; i < l; i++ {
			if m, ok := val.Index(i).Interface().(map[interface{}]interface{}); ok {
				obj := arrayOfObjects.Index(i)
				if obj.Kind() == reflect.Ptr {
					obj.Set(reflect.New(obj.Type().Elem()))
					obj = obj.Elem()
				}
				if err := setField(obj, m); err != nil {
					return err
				}
			} else {
				if err := setField(arrayOfObjects.Index(i), val.Index(i).Interface()); err != nil {
					return err
				}
			}
		}

		structFieldValue.Set(arrayOfObjects)

	case reflect.Map:
		l := val.Len()
		if l == 0 {
			break
		}

		mapType := structFieldValue.Type()

		mapOfObjects := reflect.MakeMapWithSize(mapType, l)

		// Go randomises maps. To be able to test this we need to make sure the
		// map keys always come out in the same order. So we sort them first.
		mapKeys := val.MapKeys()
		sort.Slice(mapKeys, func(i, j int) bool {
			return lessValue(mapKeys[i], mapKeys[j])
		})

		for _, k := range mapKeys {
			kValue := reflect.New(mapType.Key()).Elem()
			if err := setField(kValue, k.Interface()); err != nil {
				return err
			}

			v := val.MapIndex(k)
			vValue := reflect.New(mapType.Elem()).Elem()
			if err := setField(vValue, v.Interface()); err != nil {
				return err
			}

			mapOfObjects.SetMapIndex(kValue, vValue)
		}

		structFieldValue.Set(mapOfObjects)

	case reflect.Ptr:
		// Instantiate structFieldValue.
		structFieldValue.Set(reflect.New(structFieldValue.Type().Elem()))
		return setField(structFieldValue.Elem(), value)

	case reflect.String:
		var str string
		if val.CanInterface() {
			str = fmt.Sprintf("%v", val.Interface())
		} else {
			str = val.String()
		}
		structFieldValue.SetString(str)

	case reflect.Bool:
		structFieldValue.SetBool(val.Bool())

	default:
		structFieldValue.Set(val)
	}

	return nil
}

// https://stackoverflow.com/questions/26744873/converting-map-to-struct
func fillStruct(obj reflect.Value, m map[interface{}]interface{}) error {
	tt := obj.Type()
	for i := 0; i < obj.NumField(); i++ {
		field := obj.Field(i)
		if !field.CanSet() {
			continue
		}

		fieldType := tt.Field(i)

		if fieldType.Anonymous {
			// embedded struct
			if err := setField(field, m); err != nil {
				return err
			}
			continue
		}
		tag := fieldType.Tag.Get("php")
		if v, ok := m[tag]; tag != "" && ok {
			if err := setField(field, v); err != nil {
				return err
			}
			continue
		}
		fieldName := fieldType.Name
		lowerCaseFieldName := lowerCaseFirstLetter(fieldName)
		if v, ok := m[lowerCaseFieldName]; ok {
			if err := setField(field, v); err != nil {
				return err
			}
			continue
		}
		if v, ok := m[fieldName]; ok {
			if err := setField(field, v); err != nil {
				return err
			}
			continue
		}
	}

	return nil
}

func consumeObject(data []byte, offset int, v reflect.Value) (int, error) {
	if !checkType(data, 'O', offset) {
		return -1, errors.New("not an object")
	}

	m, offset, err := consumeObjectAsMap(data, offset)
	if err != nil {
		return -1, err
	}

	return offset, fillStruct(v, m)
}

func consumeNext(data []byte, offset int) (interface{}, int, error) {
	if offset >= len(data) {
		return nil, -1, errors.New("corrupt")
	}

	switch data[offset] {
	case 'a':
		return consumeIndexedOrAssociativeArray(data, offset)
	case 'b':
		return consumeBool(data, offset)
	case 'd':
		return consumeFloat(data, offset)
	case 'i':
		return consumeInt(data, offset)
	case 's':
		return consumeString(data, offset)
	case 'N':
		return consumeNil(data, offset)
	case 'O':
		return consumeObjectAsMap(data, offset)
	}

	return nil, -1, errors.New("can not consume type: " +
		string(data[offset:]))
}

func consumeIndexedOrAssociativeArray(data []byte, offset int) (interface{}, int, error) {
	// Sometimes we don't know if the array is going to be indexed or
	// associative until we have already started to consume it.
	originalOffset := offset

	// Try to consume it as an indexed array first.
	arr, offset, err := consumeIndexedArray(data, originalOffset)
	if err == nil {
		return arr, offset, err
	}

	// Fallback to consuming an associative array
	return consumeAssociativeArray(data, originalOffset)
}

func consumeAssociativeArray(data []byte, offset int) (map[interface{}]interface{}, int, error) {
	if !checkType(data, 'a', offset) {
		return map[interface{}]interface{}{}, -1, errors.New("not an array")
	}

	// Skip over the "a:"
	offset += 2

	rawLength, offset := consumeStringUntilByte(data, ':', offset)
	length, err := strconv.Atoi(rawLength)
	if err != nil {
		return map[interface{}]interface{}{}, -1, err
	}

	// Skip over the ":{"
	offset += 2

	result := map[interface{}]interface{}{}
	for i := 0; i < length; i++ {
		var key interface{}

		key, offset, err = consumeNext(data, offset)
		if err != nil {
			return map[interface{}]interface{}{}, -1, err
		}

		result[key], offset, err = consumeNext(data, offset)
		if err != nil {
			return map[interface{}]interface{}{}, -1, err
		}
	}

	return result, offset + 1, nil
}

func consumeAssociativeArrayIntoStruct(data []byte, offset int, v reflect.Value) (int, error) {
	m, offset, err := consumeAssociativeArray(data, offset)
	if err != nil {
		return -1, err
	}

	return offset, fillStruct(v, stringifyKeys(m).(map[interface{}]interface{}))
}

func consumeIndexedArray(data []byte, offset int) ([]interface{}, int, error) {
	if !checkType(data, 'a', offset) {
		return []interface{}{}, -1, errors.New("not an array")
	}

	rawLength, offset := consumeStringUntilByte(data, ':', offset+2)
	length, err := strconv.Atoi(rawLength)
	if err != nil {
		return []interface{}{}, -1, err
	}

	// Skip over the ":{"
	offset += 2

	result := make([]interface{}, length)
	for i := 0; i < length; i++ {
		// Even non-associative arrays (arrays that are zero-indexed)
		// still have their keys serialized. We need to read these
		// indexes to make sure we are actually decoding a slice and not
		// a map.
		var index int64
		index, offset, err = consumeInt(data, offset)
		if err != nil {
			return []interface{}{}, -1, err
		}

		if index != int64(i) {
			return []interface{}{}, -1,
				errors.New("cannot decode map as slice")
		}

		// Now we consume the value
		result[i], offset, err = consumeNext(data, offset)
		if err != nil {
			return []interface{}{}, -1, err
		}
	}

	// The +1 is for the final '}'
	return result, offset + 1, nil
}

func consumeIndexedArrayIntoStruct(data []byte, offset int, v reflect.Value) (int, error) {
	s, offset, err := consumeIndexedArray(data, offset)
	if err != nil {
		return -1, err
	}

	s = stringifyKeys(s).([]interface{})

	l := len(s)
	arrayOfObjects := reflect.MakeSlice(v.Type(), l, l)

	for i := 0; i < l; i++ {
		if m, ok := s[i].(map[interface{}]interface{}); ok {
			obj := arrayOfObjects.Index(i)
			if obj.Kind() == reflect.Ptr {
				obj.Set(reflect.New(obj.Type().Elem()))
				obj = obj.Elem()
			}
			if err := setField(obj, m); err != nil {
				return -1, err
			}
		} else {
			if err := setField(arrayOfObjects.Index(i), s[i]); err != nil {
				return -1, err
			}
		}
	}

	v.Set(arrayOfObjects)

	return offset, nil
}

// stringifyKeys recursively casts map keys into a real string but
// still stored as type interface{}, so the output still can be used
// in fillStruct() because it assumes a map[interface{}]interface{}.
func stringifyKeys(in interface{}) interface{} {
	switch x := in.(type) {
	case []interface{}:
		newSlice := make([]interface{}, len(x))
		for i, v := range x {
			newSlice[i] = stringifyKeys(v)
		}
		return newSlice

	case map[interface{}]interface{}:
		newMap := map[interface{}]interface{}{}
		for k, v := range x {
			newMap[fmt.Sprintf("%v", k)] = stringifyKeys(v)
		}
		return newMap

	default:
		return in
	}
}
