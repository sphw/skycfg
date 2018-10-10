package skycfg

import (
	"fmt"
	"reflect"

	"github.com/golang/protobuf/descriptor"
	"github.com/golang/protobuf/proto"
	descriptor_pb "github.com/golang/protobuf/protoc-gen-go/descriptor"
	"github.com/google/skylark"
)

// NewMessageType creates a Skylark value representing a named Protobuf message type.
//
// The message type must have been registered with the protobuf library, and implement
// the expected interfaces for a generated .pb.go message struct.
func newMessageType(registry unstableProtoRegistry, name string) (skylark.Value, error) {
	var goType reflect.Type
	if registry == nil {
		goType = proto.MessageType(name)
	} else {
		var err error
		goType, err = registry.UnstableProtoMessageType(name)
		if err != nil {
			return nil, err
		}
	}
	if goType == nil {
		return nil, fmt.Errorf("Protobuf message type %q not found", name)
	}

	var emptyMsg descriptor.Message
	if goType.Kind() == reflect.Ptr {
		goValue := reflect.New(goType.Elem()).Interface()
		if iface, ok := goValue.(descriptor.Message); ok {
			emptyMsg = iface
		}
	}
	if emptyMsg == nil {
		// Return a slightly useful error in case some clever person has
		// manually registered a `proto.Message` that doesn't use pointer
		// receivers.
		return nil, fmt.Errorf("InternalError: %v is not a generated proto.Message", goType)
	}
	fileDesc, msgDesc := descriptor.ForMessage(emptyMsg)
	mt := &skyProtoMessageType{
		registry: registry,
		fileDesc: fileDesc,
		msgDesc:  msgDesc,
		emptyMsg: emptyMsg,
	}
	if gotName := mt.Name(); name != gotName {
		// All the protobuf lookups are by name, so it's important that
		// buggy self-registered protobuf types don't get mixed in.
		return nil, fmt.Errorf("InternalError: %v has unexpected protobuf type name %q (wanted %q)", goType, gotName, name)
	}
	return mt, nil

}

// A Skylark built-in type representing a Protobuf message type. This is the
// message type itself rather than any particular message value.
type skyProtoMessageType struct {
	registry unstableProtoRegistry
	fileDesc *descriptor_pb.FileDescriptorProto
	msgDesc  *descriptor_pb.DescriptorProto

	// An empty protobuf message of the appropriate type.
	emptyMsg proto.Message
}

var _ skylark.HasAttrs = (*skyProtoMessageType)(nil)
var _ skylark.Callable = (*skyProtoMessageType)(nil)

func (mt *skyProtoMessageType) String() string {
	return fmt.Sprintf("<proto.MessageType %q>", mt.Name())
}
func (mt *skyProtoMessageType) Type() string        { return "proto.MessageType" }
func (mt *skyProtoMessageType) Freeze()             {}
func (mt *skyProtoMessageType) Truth() skylark.Bool { return skylark.True }
func (mt *skyProtoMessageType) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: %s", mt.Type())
}

func (mt *skyProtoMessageType) Name() string {
	if mt.fileDesc.GetPackage() == "" {
		return mt.msgDesc.GetName()
	}
	return fmt.Sprintf("%s.%s", mt.fileDesc.GetPackage(), mt.msgDesc.GetName())
}

func (mt *skyProtoMessageType) Attr(attrName string) (skylark.Value, error) {
	return newMessageType(mt.registry, fmt.Sprintf("%s.%s", mt.Name(), attrName))
}

func (mt *skyProtoMessageType) AttrNames() []string {
	// TODO: Implement when go-protobuf gains support for listing the
	// registered message types in a Protobuf package. Since `dir(msgType)`
	// should return the names of its nested messages, this needs to be
	// implemented as a filtered version of `skyProtoPackage.AttrNames()`
	// that checks for `HasPrefix(msgName, mt.Name() + ".")`.
	//
	// https://github.com/golang/protobuf/issues/623
	return nil
}

func (mt *skyProtoMessageType) Call(thread *skylark.Thread, args skylark.Tuple, kwargs []skylark.Tuple) (skylark.Value, error) {
	// This is semantically the constructor of a protobuf message, and we
	// want it to accept only kwargs (where keys are protobuf field names).
	// Inject a useful error message if a user tries to pass positional args.
	if err := skylark.UnpackPositionalArgs(mt.Name(), args, nil, 0); err != nil {
		return nil, err
	}

	wrapper := newSkyProtoMessage(proto.Clone(mt.emptyMsg))

	// Parse the kwarg set into a map[string]skylark.Value, containing one
	// entry for each provided kwarg. Keys are the original protobuf field names.
	// This lets the skylark kwarg parser handle most of the error reporting,
	// except type errors which are deferred until later.
	var parserPairs []interface{}
	parsedKwargs := make(map[string]*skylark.Value, len(kwargs))

	for _, field := range wrapper.fields {
		v := new(skylark.Value)
		parsedKwargs[field.OrigName] = v
		parserPairs = append(parserPairs, field.OrigName+"?", v)
	}
	if err := skylark.UnpackArgs(mt.Name(), nil, kwargs, parserPairs...); err != nil {
		return nil, err
	}
	for fieldName, skylarkValue := range parsedKwargs {
		if *skylarkValue == nil {
			continue
		}
		if err := wrapper.SetField(fieldName, *skylarkValue); err != nil {
			return nil, err
		}
	}
	return wrapper, nil
}