// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.27.1
// 	protoc        v3.21.9
// source: ipc.proto

package ipc

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type GetDebugInfoRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// Types that are assignable to Request:
	//	*GetDebugInfoRequest_GstPipelineDot
	//	*GetDebugInfoRequest_Pprof
	Request isGetDebugInfoRequest_Request `protobuf_oneof:"request"`
}

func (x *GetDebugInfoRequest) Reset() {
	*x = GetDebugInfoRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_ipc_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *GetDebugInfoRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetDebugInfoRequest) ProtoMessage() {}

func (x *GetDebugInfoRequest) ProtoReflect() protoreflect.Message {
	mi := &file_ipc_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetDebugInfoRequest.ProtoReflect.Descriptor instead.
func (*GetDebugInfoRequest) Descriptor() ([]byte, []int) {
	return file_ipc_proto_rawDescGZIP(), []int{0}
}

func (m *GetDebugInfoRequest) GetRequest() isGetDebugInfoRequest_Request {
	if m != nil {
		return m.Request
	}
	return nil
}

func (x *GetDebugInfoRequest) GetGstPipelineDot() *GstPipelineDebugDotRequest {
	if x, ok := x.GetRequest().(*GetDebugInfoRequest_GstPipelineDot); ok {
		return x.GstPipelineDot
	}
	return nil
}

func (x *GetDebugInfoRequest) GetPprof() *PprofRequest {
	if x, ok := x.GetRequest().(*GetDebugInfoRequest_Pprof); ok {
		return x.Pprof
	}
	return nil
}

type isGetDebugInfoRequest_Request interface {
	isGetDebugInfoRequest_Request()
}

type GetDebugInfoRequest_GstPipelineDot struct {
	GstPipelineDot *GstPipelineDebugDotRequest `protobuf:"bytes,1,opt,name=gst_pipeline_dot,json=gstPipelineDot,proto3,oneof"`
}

type GetDebugInfoRequest_Pprof struct {
	Pprof *PprofRequest `protobuf:"bytes,2,opt,name=pprof,proto3,oneof"`
}

func (*GetDebugInfoRequest_GstPipelineDot) isGetDebugInfoRequest_Request() {}

func (*GetDebugInfoRequest_Pprof) isGetDebugInfoRequest_Request() {}

type GetDebugInfoResponse struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// Types that are assignable to Response:
	//	*GetDebugInfoResponse_GstPipelineDot
	//	*GetDebugInfoResponse_Pprof
	Response isGetDebugInfoResponse_Response `protobuf_oneof:"response"`
}

func (x *GetDebugInfoResponse) Reset() {
	*x = GetDebugInfoResponse{}
	if protoimpl.UnsafeEnabled {
		mi := &file_ipc_proto_msgTypes[1]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *GetDebugInfoResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetDebugInfoResponse) ProtoMessage() {}

func (x *GetDebugInfoResponse) ProtoReflect() protoreflect.Message {
	mi := &file_ipc_proto_msgTypes[1]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetDebugInfoResponse.ProtoReflect.Descriptor instead.
func (*GetDebugInfoResponse) Descriptor() ([]byte, []int) {
	return file_ipc_proto_rawDescGZIP(), []int{1}
}

func (m *GetDebugInfoResponse) GetResponse() isGetDebugInfoResponse_Response {
	if m != nil {
		return m.Response
	}
	return nil
}

func (x *GetDebugInfoResponse) GetGstPipelineDot() *GstPipelineDebugDotResponse {
	if x, ok := x.GetResponse().(*GetDebugInfoResponse_GstPipelineDot); ok {
		return x.GstPipelineDot
	}
	return nil
}

func (x *GetDebugInfoResponse) GetPprof() *PprofResponse {
	if x, ok := x.GetResponse().(*GetDebugInfoResponse_Pprof); ok {
		return x.Pprof
	}
	return nil
}

type isGetDebugInfoResponse_Response interface {
	isGetDebugInfoResponse_Response()
}

type GetDebugInfoResponse_GstPipelineDot struct {
	GstPipelineDot *GstPipelineDebugDotResponse `protobuf:"bytes,1,opt,name=gst_pipeline_dot,json=gstPipelineDot,proto3,oneof"`
}

type GetDebugInfoResponse_Pprof struct {
	Pprof *PprofResponse `protobuf:"bytes,2,opt,name=pprof,proto3,oneof"`
}

func (*GetDebugInfoResponse_GstPipelineDot) isGetDebugInfoResponse_Response() {}

func (*GetDebugInfoResponse_Pprof) isGetDebugInfoResponse_Response() {}

type GstPipelineDebugDotRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

func (x *GstPipelineDebugDotRequest) Reset() {
	*x = GstPipelineDebugDotRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_ipc_proto_msgTypes[2]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *GstPipelineDebugDotRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GstPipelineDebugDotRequest) ProtoMessage() {}

func (x *GstPipelineDebugDotRequest) ProtoReflect() protoreflect.Message {
	mi := &file_ipc_proto_msgTypes[2]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GstPipelineDebugDotRequest.ProtoReflect.Descriptor instead.
func (*GstPipelineDebugDotRequest) Descriptor() ([]byte, []int) {
	return file_ipc_proto_rawDescGZIP(), []int{2}
}

type GstPipelineDebugDotResponse struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	DotFile string `protobuf:"bytes,1,opt,name=dot_file,json=dotFile,proto3" json:"dot_file,omitempty"`
}

func (x *GstPipelineDebugDotResponse) Reset() {
	*x = GstPipelineDebugDotResponse{}
	if protoimpl.UnsafeEnabled {
		mi := &file_ipc_proto_msgTypes[3]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *GstPipelineDebugDotResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GstPipelineDebugDotResponse) ProtoMessage() {}

func (x *GstPipelineDebugDotResponse) ProtoReflect() protoreflect.Message {
	mi := &file_ipc_proto_msgTypes[3]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GstPipelineDebugDotResponse.ProtoReflect.Descriptor instead.
func (*GstPipelineDebugDotResponse) Descriptor() ([]byte, []int) {
	return file_ipc_proto_rawDescGZIP(), []int{3}
}

func (x *GstPipelineDebugDotResponse) GetDotFile() string {
	if x != nil {
		return x.DotFile
	}
	return ""
}

type PprofRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	ProfileName string `protobuf:"bytes,1,opt,name=profile_name,json=profileName,proto3" json:"profile_name,omitempty"`
	Timeout     int32  `protobuf:"varint,2,opt,name=timeout,proto3" json:"timeout,omitempty"`
	Debug       int32  `protobuf:"varint,3,opt,name=debug,proto3" json:"debug,omitempty"`
}

func (x *PprofRequest) Reset() {
	*x = PprofRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_ipc_proto_msgTypes[4]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *PprofRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*PprofRequest) ProtoMessage() {}

func (x *PprofRequest) ProtoReflect() protoreflect.Message {
	mi := &file_ipc_proto_msgTypes[4]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use PprofRequest.ProtoReflect.Descriptor instead.
func (*PprofRequest) Descriptor() ([]byte, []int) {
	return file_ipc_proto_rawDescGZIP(), []int{4}
}

func (x *PprofRequest) GetProfileName() string {
	if x != nil {
		return x.ProfileName
	}
	return ""
}

func (x *PprofRequest) GetTimeout() int32 {
	if x != nil {
		return x.Timeout
	}
	return 0
}

func (x *PprofRequest) GetDebug() int32 {
	if x != nil {
		return x.Debug
	}
	return 0
}

type PprofResponse struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	PprofFile []byte `protobuf:"bytes,1,opt,name=pprof_file,json=pprofFile,proto3" json:"pprof_file,omitempty"`
}

func (x *PprofResponse) Reset() {
	*x = PprofResponse{}
	if protoimpl.UnsafeEnabled {
		mi := &file_ipc_proto_msgTypes[5]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *PprofResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*PprofResponse) ProtoMessage() {}

func (x *PprofResponse) ProtoReflect() protoreflect.Message {
	mi := &file_ipc_proto_msgTypes[5]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use PprofResponse.ProtoReflect.Descriptor instead.
func (*PprofResponse) Descriptor() ([]byte, []int) {
	return file_ipc_proto_rawDescGZIP(), []int{5}
}

func (x *PprofResponse) GetPprofFile() []byte {
	if x != nil {
		return x.PprofFile
	}
	return nil
}

var File_ipc_proto protoreflect.FileDescriptor

var file_ipc_proto_rawDesc = []byte{
	0x0a, 0x09, 0x69, 0x70, 0x63, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x12, 0x03, 0x69, 0x70, 0x63,
	0x22, 0x98, 0x01, 0x0a, 0x13, 0x47, 0x65, 0x74, 0x44, 0x65, 0x62, 0x75, 0x67, 0x49, 0x6e, 0x66,
	0x6f, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x12, 0x4b, 0x0a, 0x10, 0x67, 0x73, 0x74, 0x5f,
	0x70, 0x69, 0x70, 0x65, 0x6c, 0x69, 0x6e, 0x65, 0x5f, 0x64, 0x6f, 0x74, 0x18, 0x01, 0x20, 0x01,
	0x28, 0x0b, 0x32, 0x1f, 0x2e, 0x69, 0x70, 0x63, 0x2e, 0x47, 0x73, 0x74, 0x50, 0x69, 0x70, 0x65,
	0x6c, 0x69, 0x6e, 0x65, 0x44, 0x65, 0x62, 0x75, 0x67, 0x44, 0x6f, 0x74, 0x52, 0x65, 0x71, 0x75,
	0x65, 0x73, 0x74, 0x48, 0x00, 0x52, 0x0e, 0x67, 0x73, 0x74, 0x50, 0x69, 0x70, 0x65, 0x6c, 0x69,
	0x6e, 0x65, 0x44, 0x6f, 0x74, 0x12, 0x29, 0x0a, 0x05, 0x70, 0x70, 0x72, 0x6f, 0x66, 0x18, 0x02,
	0x20, 0x01, 0x28, 0x0b, 0x32, 0x11, 0x2e, 0x69, 0x70, 0x63, 0x2e, 0x50, 0x70, 0x72, 0x6f, 0x66,
	0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x48, 0x00, 0x52, 0x05, 0x70, 0x70, 0x72, 0x6f, 0x66,
	0x42, 0x09, 0x0a, 0x07, 0x72, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x22, 0x9c, 0x01, 0x0a, 0x14,
	0x47, 0x65, 0x74, 0x44, 0x65, 0x62, 0x75, 0x67, 0x49, 0x6e, 0x66, 0x6f, 0x52, 0x65, 0x73, 0x70,
	0x6f, 0x6e, 0x73, 0x65, 0x12, 0x4c, 0x0a, 0x10, 0x67, 0x73, 0x74, 0x5f, 0x70, 0x69, 0x70, 0x65,
	0x6c, 0x69, 0x6e, 0x65, 0x5f, 0x64, 0x6f, 0x74, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x20,
	0x2e, 0x69, 0x70, 0x63, 0x2e, 0x47, 0x73, 0x74, 0x50, 0x69, 0x70, 0x65, 0x6c, 0x69, 0x6e, 0x65,
	0x44, 0x65, 0x62, 0x75, 0x67, 0x44, 0x6f, 0x74, 0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65,
	0x48, 0x00, 0x52, 0x0e, 0x67, 0x73, 0x74, 0x50, 0x69, 0x70, 0x65, 0x6c, 0x69, 0x6e, 0x65, 0x44,
	0x6f, 0x74, 0x12, 0x2a, 0x0a, 0x05, 0x70, 0x70, 0x72, 0x6f, 0x66, 0x18, 0x02, 0x20, 0x01, 0x28,
	0x0b, 0x32, 0x12, 0x2e, 0x69, 0x70, 0x63, 0x2e, 0x50, 0x70, 0x72, 0x6f, 0x66, 0x52, 0x65, 0x73,
	0x70, 0x6f, 0x6e, 0x73, 0x65, 0x48, 0x00, 0x52, 0x05, 0x70, 0x70, 0x72, 0x6f, 0x66, 0x42, 0x0a,
	0x0a, 0x08, 0x72, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x22, 0x1c, 0x0a, 0x1a, 0x47, 0x73,
	0x74, 0x50, 0x69, 0x70, 0x65, 0x6c, 0x69, 0x6e, 0x65, 0x44, 0x65, 0x62, 0x75, 0x67, 0x44, 0x6f,
	0x74, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x22, 0x38, 0x0a, 0x1b, 0x47, 0x73, 0x74, 0x50,
	0x69, 0x70, 0x65, 0x6c, 0x69, 0x6e, 0x65, 0x44, 0x65, 0x62, 0x75, 0x67, 0x44, 0x6f, 0x74, 0x52,
	0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x12, 0x19, 0x0a, 0x08, 0x64, 0x6f, 0x74, 0x5f, 0x66,
	0x69, 0x6c, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x07, 0x64, 0x6f, 0x74, 0x46, 0x69,
	0x6c, 0x65, 0x22, 0x61, 0x0a, 0x0c, 0x50, 0x70, 0x72, 0x6f, 0x66, 0x52, 0x65, 0x71, 0x75, 0x65,
	0x73, 0x74, 0x12, 0x21, 0x0a, 0x0c, 0x70, 0x72, 0x6f, 0x66, 0x69, 0x6c, 0x65, 0x5f, 0x6e, 0x61,
	0x6d, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0b, 0x70, 0x72, 0x6f, 0x66, 0x69, 0x6c,
	0x65, 0x4e, 0x61, 0x6d, 0x65, 0x12, 0x18, 0x0a, 0x07, 0x74, 0x69, 0x6d, 0x65, 0x6f, 0x75, 0x74,
	0x18, 0x02, 0x20, 0x01, 0x28, 0x05, 0x52, 0x07, 0x74, 0x69, 0x6d, 0x65, 0x6f, 0x75, 0x74, 0x12,
	0x14, 0x0a, 0x05, 0x64, 0x65, 0x62, 0x75, 0x67, 0x18, 0x03, 0x20, 0x01, 0x28, 0x05, 0x52, 0x05,
	0x64, 0x65, 0x62, 0x75, 0x67, 0x22, 0x2e, 0x0a, 0x0d, 0x50, 0x70, 0x72, 0x6f, 0x66, 0x52, 0x65,
	0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x12, 0x1d, 0x0a, 0x0a, 0x70, 0x70, 0x72, 0x6f, 0x66, 0x5f,
	0x66, 0x69, 0x6c, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0c, 0x52, 0x09, 0x70, 0x70, 0x72, 0x6f,
	0x66, 0x46, 0x69, 0x6c, 0x65, 0x32, 0x56, 0x0a, 0x0d, 0x45, 0x67, 0x72, 0x65, 0x73, 0x73, 0x48,
	0x61, 0x6e, 0x64, 0x6c, 0x65, 0x72, 0x12, 0x45, 0x0a, 0x0c, 0x47, 0x65, 0x74, 0x44, 0x65, 0x62,
	0x75, 0x67, 0x49, 0x6e, 0x66, 0x6f, 0x12, 0x18, 0x2e, 0x69, 0x70, 0x63, 0x2e, 0x47, 0x65, 0x74,
	0x44, 0x65, 0x62, 0x75, 0x67, 0x49, 0x6e, 0x66, 0x6f, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74,
	0x1a, 0x19, 0x2e, 0x69, 0x70, 0x63, 0x2e, 0x47, 0x65, 0x74, 0x44, 0x65, 0x62, 0x75, 0x67, 0x49,
	0x6e, 0x66, 0x6f, 0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x22, 0x00, 0x42, 0x23, 0x5a,
	0x21, 0x67, 0x69, 0x74, 0x68, 0x75, 0x62, 0x2e, 0x63, 0x6f, 0x6d, 0x2f, 0x6c, 0x69, 0x76, 0x65,
	0x6b, 0x69, 0x74, 0x2f, 0x65, 0x67, 0x72, 0x65, 0x73, 0x73, 0x2f, 0x70, 0x6b, 0x67, 0x2f, 0x69,
	0x70, 0x63, 0x62, 0x06, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x33,
}

var (
	file_ipc_proto_rawDescOnce sync.Once
	file_ipc_proto_rawDescData = file_ipc_proto_rawDesc
)

func file_ipc_proto_rawDescGZIP() []byte {
	file_ipc_proto_rawDescOnce.Do(func() {
		file_ipc_proto_rawDescData = protoimpl.X.CompressGZIP(file_ipc_proto_rawDescData)
	})
	return file_ipc_proto_rawDescData
}

var file_ipc_proto_msgTypes = make([]protoimpl.MessageInfo, 6)
var file_ipc_proto_goTypes = []interface{}{
	(*GetDebugInfoRequest)(nil),         // 0: ipc.GetDebugInfoRequest
	(*GetDebugInfoResponse)(nil),        // 1: ipc.GetDebugInfoResponse
	(*GstPipelineDebugDotRequest)(nil),  // 2: ipc.GstPipelineDebugDotRequest
	(*GstPipelineDebugDotResponse)(nil), // 3: ipc.GstPipelineDebugDotResponse
	(*PprofRequest)(nil),                // 4: ipc.PprofRequest
	(*PprofResponse)(nil),               // 5: ipc.PprofResponse
}
var file_ipc_proto_depIdxs = []int32{
	2, // 0: ipc.GetDebugInfoRequest.gst_pipeline_dot:type_name -> ipc.GstPipelineDebugDotRequest
	4, // 1: ipc.GetDebugInfoRequest.pprof:type_name -> ipc.PprofRequest
	3, // 2: ipc.GetDebugInfoResponse.gst_pipeline_dot:type_name -> ipc.GstPipelineDebugDotResponse
	5, // 3: ipc.GetDebugInfoResponse.pprof:type_name -> ipc.PprofResponse
	0, // 4: ipc.EgressHandler.GetDebugInfo:input_type -> ipc.GetDebugInfoRequest
	1, // 5: ipc.EgressHandler.GetDebugInfo:output_type -> ipc.GetDebugInfoResponse
	5, // [5:6] is the sub-list for method output_type
	4, // [4:5] is the sub-list for method input_type
	4, // [4:4] is the sub-list for extension type_name
	4, // [4:4] is the sub-list for extension extendee
	0, // [0:4] is the sub-list for field type_name
}

func init() { file_ipc_proto_init() }
func file_ipc_proto_init() {
	if File_ipc_proto != nil {
		return
	}
	if !protoimpl.UnsafeEnabled {
		file_ipc_proto_msgTypes[0].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*GetDebugInfoRequest); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_ipc_proto_msgTypes[1].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*GetDebugInfoResponse); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_ipc_proto_msgTypes[2].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*GstPipelineDebugDotRequest); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_ipc_proto_msgTypes[3].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*GstPipelineDebugDotResponse); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_ipc_proto_msgTypes[4].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*PprofRequest); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_ipc_proto_msgTypes[5].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*PprofResponse); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
	}
	file_ipc_proto_msgTypes[0].OneofWrappers = []interface{}{
		(*GetDebugInfoRequest_GstPipelineDot)(nil),
		(*GetDebugInfoRequest_Pprof)(nil),
	}
	file_ipc_proto_msgTypes[1].OneofWrappers = []interface{}{
		(*GetDebugInfoResponse_GstPipelineDot)(nil),
		(*GetDebugInfoResponse_Pprof)(nil),
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_ipc_proto_rawDesc,
			NumEnums:      0,
			NumMessages:   6,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_ipc_proto_goTypes,
		DependencyIndexes: file_ipc_proto_depIdxs,
		MessageInfos:      file_ipc_proto_msgTypes,
	}.Build()
	File_ipc_proto = out.File
	file_ipc_proto_rawDesc = nil
	file_ipc_proto_goTypes = nil
	file_ipc_proto_depIdxs = nil
}
