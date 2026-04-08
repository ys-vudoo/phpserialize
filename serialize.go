package phpserialize

import (
	"bytes"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// MarshalOptions must be provided when invoking Marshal(). Use
// DefaultMarshalOptions() for sensible defaults.
type MarshalOptions struct {
	// If this is true, then all struct names will be stripped from objects
	// and "stdClass" will be used instead. The default value is false.
	OnlyStdClass bool
	// If this is true, then a struct will be marshalled as if it is a map.
	MarshalStructAsMap bool
}

// DefaultMarshalOptions will create a new instance of MarshalOptions with
// sensible defaults. See MarshalOptions for a full description of options.
func DefaultMarshalOptions() *MarshalOptions {
	options := new(MarshalOptions)
	options.OnlyStdClass = false

	return options
}

// MarshalBool returns the bytes to represent a PHP serialized bool value. This
// would be the equivalent to running:
//
//	echo serialize(false);
//	// b:0;
//
// The same result would be returned by marshalling a boolean value:
//
//	Marshal(true)
func MarshalBool(value bool) []byte {
	if value {
		return []byte("b:1;")
	}

	return []byte("b:0;")
}

// MarshalInt returns the bytes to represent a PHP serialized integer value.
// This would be the equivalent to running:
//
//	echo serialize(123);
//	// i:123;
//
// The same result would be returned by marshalling an integer value:
//
//	Marshal(123)
func MarshalInt(value int64) []byte {
	return append(strconv.AppendInt([]byte("i:"), value, 10), ';')
}

// MarshalUint is provided for compatibility with unsigned types in Go. It works
// the same way as MarshalInt.
func MarshalUint(value uint64) []byte {
	return append(strconv.AppendUint([]byte("i:"), value, 10), ';')
}

// MarshalFloat returns the bytes to represent a PHP serialized floating-point
// value. This would be the equivalent to running:
//
//	echo serialize(1.23);
//	// d:1.23;
//
// The bitSize should represent the size of the float. This makes conversion to
// a string value more accurate, for example:
//
//	// float64 is implicit for literals
//	MarshalFloat(1.23, 64)
//
//	// If the original value was cast from a float32
//	f := float32(1.23)
//	MarshalFloat(float64(f), 32)
//
// The same result would be returned by marshalling a floating-point value:
//
//	Marshal(1.23)
func MarshalFloat(value float64, bitSize int) []byte {
	return append(strconv.AppendFloat([]byte("d:"), value, 'f', -1, bitSize), ';')
}

// MarshalString returns the bytes to represent a PHP serialized string value.
// This would be the equivalent to running:
//
//	echo serialize('Hello world');
//	// s:11:"Hello world";
//
// The same result would be returned by marshalling a string value:
//
//	Marshal('Hello world')
//
// One important distinction is that PHP stores binary data in strings. See
// MarshalBytes for more information.
func MarshalString(value string) []byte {
	var buffer bytes.Buffer
	marshalString(value, &buffer)
	return buffer.Bytes()
}

func marshalString(value string, buffer *bytes.Buffer) {
	// As far as I can tell only the single-quote is escaped. Not even the
	// backslash itself is escaped. Weird. See escapeTests for more information.
	value = strings.ReplaceAll(value, "'", "\\'")

	buffer.WriteString(`s:`)
	buffer.WriteString(strconv.FormatUint(uint64(len(value)), 10))
	buffer.WriteString(`:"`)
	buffer.WriteString(value)
	buffer.WriteString(`";`)
}

// MarshalBytes returns the bytes to represent a PHP serialized string value
// that contains binary data. This is because PHP does not have a distinct type
// for binary data.
//
// This can cause some confusion when decoding the value as it will want to
// unmarshal as a string type. The Unmarshal() function will be sensitive to
// this condition and allow either a string or []byte when unserializing a PHP
// string.
func MarshalBytes(value []byte) []byte {
	var buffer bytes.Buffer
	marshalBytes(value, &buffer)
	return buffer.Bytes()
}

func marshalBytes(value []byte, buffer *bytes.Buffer) {
	fmt.Fprintf(buffer, "s:%d:\"", len(value))
	for _, c := range value {
		fmt.Fprintf(buffer, "\\x%02x", c)
	}
	buffer.WriteString("\";")
}

// MarshalNil returns the bytes to represent a PHP serialized null value.
// This would be the equivalent to running:
//
//	echo serialize(null);
//	// N;
//
// Unlike the other specific Marshal functions it does not take an argument
// because the output is a constant value.
func MarshalNil() []byte {
	return []byte("N;")
}

// MarshalStruct returns the bytes that represent a PHP encoded class from a
// struct or pointer to a struct.
//
// Fields that are not exported (starting with a lowercase letter) will not be
// present in the output. All fields that appear in the output will have their
// first letter converted to lowercase. Any other uppercase letters in the field
// name are maintained. At the moment there is no way to change this behaviour,
// unlike other marshallers that use a tag on the field.
func MarshalStruct(input interface{}, options *MarshalOptions) ([]byte, error) {
	var buffer bytes.Buffer
	if _, err := marshalStruct(input, options, false, &buffer); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func marshalStruct(input interface{}, options *MarshalOptions, embeded bool, buffer *bytes.Buffer) (int, error) {
	value := reflect.ValueOf(input)
	typeOfValue := value.Type()

	// Some of the fields in the struct may not be visible (unexported). We
	// need to make sure we count all the visible ones for the final result.
	visibleFieldCount := 0

	var writes [](func() error)

	for i := 0; i < value.NumField(); i++ {
		f := value.Field(i)
		ft := typeOfValue.Field(i)

		if !f.CanInterface() {
			// This is an unexported field, we cannot read it.
			continue
		}

		fieldName, fieldOptions := parseTag(ft.Tag.Get("php"))
		if fieldOptions.Contains("omitnilptr") && f.Kind() == reflect.Ptr && f.IsNil() {
			continue
		}
		if fieldName == "-" {
			continue
		}

		if ft.Anonymous && options.MarshalStructAsMap {
			var newVal any
			if f.Kind() == reflect.Struct {
				// the field is embedded struct
				newVal = f.Interface()
			} else if f.Kind() == reflect.Ptr && f.Elem().Kind() == reflect.Struct {
				// the field is embedded struct ptr
				newVal = f.Elem().Interface()
			}

			var newBuff bytes.Buffer
			fields, err := marshalStruct(newVal, options, true, &newBuff)
			if err != nil {
				return -1, err
			}

			writes = append(writes, func() error {
				buffer.Write(newBuff.Bytes())
				return nil
			})

			visibleFieldCount += fields
			continue
		}

		visibleFieldCount++

		if fieldName == "" {
			fieldName = ft.Name
			if !options.MarshalStructAsMap {
				fieldName = lowerCaseFirstLetter(fieldName)
			}
		}

		// need to do write to buffer later after visibleFieldCount calculated
		writes = append(writes, func() error {
			buffer.Write(MarshalString(fieldName))
			if err := marshal(f.Interface(), options, buffer); err != nil {
				return err
			}
			return nil
		})
	}

	if !embeded {
		if options.MarshalStructAsMap {
			fmt.Fprintf(buffer, "a:%d:{", visibleFieldCount)
		} else {
			className := reflect.ValueOf(input).Type().Name()
			if options.OnlyStdClass {
				className = "stdClass"
			}
			fmt.Fprintf(buffer, "O:%d:\"%s\":%d:{", len(className), className, visibleFieldCount)
		}
	}

	for _, w := range writes {
		if err := w(); err != nil {
			return -1, err
		}
	}

	if !embeded {
		buffer.WriteString("}")
	}

	return visibleFieldCount, nil
}

// Marshal is the canonical way to perform the equivalent of serialize() in PHP.
// It can handle encoding scalar types, slices and maps.
func Marshal(input interface{}, options *MarshalOptions) ([]byte, error) {
	var buffer bytes.Buffer
	if err := marshal(input, options, &buffer); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func marshal(input interface{}, options *MarshalOptions, buffer *bytes.Buffer) error {
	if options == nil {
		options = DefaultMarshalOptions()
	}

	// []byte is a special case because all strings (binary and otherwise)
	// are handled as strings in PHP.
	if bytesToEncode, ok := input.([]byte); ok {
		marshalBytes(bytesToEncode, buffer)
		return nil
	}

	// Nil is another special case because it is typeless and must be
	// handled before trying to determine the type.
	if input == nil {
		buffer.Write(MarshalNil())
		return nil
	}

	// Otherwise we need to decide if it is a scalar value, map or slice.
	value := reflect.ValueOf(input)
	switch value.Kind() {
	case reflect.Bool:
		buffer.Write(MarshalBool(value.Bool()))
		return nil

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		buffer.Write(MarshalInt(value.Int()))
		return nil

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		buffer.Write(MarshalUint(value.Uint()))
		return nil

	case reflect.Float32:
		buffer.Write(MarshalFloat(value.Float(), 32))
		return nil

	case reflect.Float64:
		buffer.Write(MarshalFloat(value.Float(), 64))
		return nil

	case reflect.String:
		buffer.Write(MarshalString(value.String()))
		return nil

	case reflect.Slice:
		return marshalSlice(value.Interface(), options, buffer)

	case reflect.Map:
		return marshalMap(value.Interface(), options, buffer)

	case reflect.Struct:
		_, err := marshalStruct(input, options, false, buffer)
		return err

	case reflect.Pointer:
		if value.IsNil() {
			buffer.Write(MarshalNil())
			return nil
		}
		return marshal(value.Elem().Interface(), options, buffer)

	default:
		return fmt.Errorf("can not encode: %T", input)
	}
}

func marshalSlice(input interface{}, options *MarshalOptions, buffer *bytes.Buffer) error {
	s := reflect.ValueOf(input)

	fmt.Fprintf(buffer, "a:%d:{", s.Len())

	for i := 0; i < s.Len(); i++ {
		if err := marshal(i, options, buffer); err != nil {
			return err
		}

		if err := marshal(s.Index(i).Interface(), options, buffer); err != nil {
			return err
		}
	}

	buffer.WriteString("}")

	return nil
}

func marshalMap(input interface{}, options *MarshalOptions, buffer *bytes.Buffer) error {
	s := reflect.ValueOf(input)

	fmt.Fprintf(buffer, "a:%d:{", s.Len())

	// Go randomises maps. To be able to test this we need to make sure the
	// map keys always come out in the same order. So we sort them first.
	mapKeys := s.MapKeys()
	sort.Slice(mapKeys, func(i, j int) bool {
		return lessValue(mapKeys[i], mapKeys[j])
	})

	for _, mapKey := range mapKeys {
		if err := marshal(mapKey.Interface(), options, buffer); err != nil {
			return err
		}

		if err := marshal(s.MapIndex(mapKey).Interface(), options, buffer); err != nil {
			return err
		}
	}

	buffer.WriteString("}")

	return nil
}

func lowerCaseFirstLetter(s string) string {
	return strings.ToLower(s[0:1]) + s[1:]
}
