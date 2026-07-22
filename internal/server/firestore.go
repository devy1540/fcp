package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"github.com/hjyoon/fcp/internal/state"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type firestoreServer struct {
	firestorepb.UnimplementedFirestoreServer
	store *state.Store
}

func newFirestoreServer(store *state.Store) *firestoreServer {
	return &firestoreServer{store: store}
}

func (s *firestoreServer) GetDocument(_ context.Context, request *firestorepb.GetDocumentRequest) (*firestorepb.Document, error) {
	document, err := s.loadDocument(request.GetName())
	if errors.Is(err, state.ErrFirestoreDocumentNotFound) {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return applyDocumentMask(document, request.GetMask()), nil
}

func (s *firestoreServer) ListDocuments(_ context.Context, request *firestorepb.ListDocumentsRequest) (*firestorepb.ListDocumentsResponse, error) {
	prefix := strings.TrimSuffix(request.GetParent(), "/") + "/" + request.GetCollectionId() + "/"
	all := s.store.ListFirestoreDocuments(prefix)
	after := state.DecodePageToken(request.GetPageToken())
	pageSize := int(request.GetPageSize())
	if pageSize <= 0 {
		pageSize = 100
	}
	response := &firestorepb.ListDocumentsResponse{}
	for _, stored := range all {
		relative := strings.TrimPrefix(stored.Name, prefix)
		if strings.Contains(relative, "/") || stored.Name <= after {
			continue
		}
		if len(response.Documents) >= pageSize {
			response.NextPageToken = state.EncodePageToken(response.Documents[len(response.Documents)-1].GetName())
			break
		}
		document, err := decodeStoredDocument(stored)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		response.Documents = append(response.Documents, applyDocumentMask(document, request.GetMask()))
	}
	return response, nil
}

func (s *firestoreServer) CreateDocument(_ context.Context, request *firestorepb.CreateDocumentRequest) (*firestorepb.Document, error) {
	if request.GetDocumentId() == "" {
		return nil, status.Error(codes.InvalidArgument, "document_id is required")
	}
	document := proto.Clone(request.GetDocument()).(*firestorepb.Document)
	document.Name = strings.TrimSuffix(request.GetParent(), "/") + "/" + request.GetCollectionId() + "/" + request.GetDocumentId()
	var result *firestorepb.Document
	err := s.store.MutateFirestore(func(documents map[string]*state.FirestoreDocument, now time.Time) error {
		if _, exists := documents[document.Name]; exists {
			return status.Error(codes.AlreadyExists, "document already exists")
		}
		created, _, err := applyFirestoreWrite(documents, &firestorepb.Write{
			Operation:       &firestorepb.Write_Update{Update: document},
			CurrentDocument: &firestorepb.Precondition{ConditionType: &firestorepb.Precondition_Exists{Exists: false}},
		}, now)
		result = created
		return err
	})
	if err != nil {
		return nil, normalizeFirestoreError(err)
	}
	return applyDocumentMask(result, request.GetMask()), nil
}

func (s *firestoreServer) UpdateDocument(_ context.Context, request *firestorepb.UpdateDocumentRequest) (*firestorepb.Document, error) {
	if request.GetDocument().GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "document name is required")
	}
	var result *firestorepb.Document
	err := s.store.MutateFirestore(func(documents map[string]*state.FirestoreDocument, now time.Time) error {
		updated, _, err := applyFirestoreWrite(documents, &firestorepb.Write{
			Operation:       &firestorepb.Write_Update{Update: request.GetDocument()},
			UpdateMask:      request.GetUpdateMask(),
			CurrentDocument: request.GetCurrentDocument(),
		}, now)
		result = updated
		return err
	})
	if err != nil {
		return nil, normalizeFirestoreError(err)
	}
	return applyDocumentMask(result, request.GetMask()), nil
}

func (s *firestoreServer) DeleteDocument(_ context.Context, request *firestorepb.DeleteDocumentRequest) (*emptypb.Empty, error) {
	err := s.store.MutateFirestore(func(documents map[string]*state.FirestoreDocument, now time.Time) error {
		_, _, err := applyFirestoreWrite(documents, &firestorepb.Write{
			Operation:       &firestorepb.Write_Delete{Delete: request.GetName()},
			CurrentDocument: request.GetCurrentDocument(),
		}, now)
		return err
	})
	if err != nil {
		return nil, normalizeFirestoreError(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *firestoreServer) BatchGetDocuments(request *firestorepb.BatchGetDocumentsRequest, stream firestorepb.Firestore_BatchGetDocumentsServer) error {
	now := timestamppb.Now()
	transaction := request.GetTransaction()
	if request.GetNewTransaction() != nil {
		transaction = []byte(newTransactionID())
	}
	for i, name := range request.GetDocuments() {
		response := &firestorepb.BatchGetDocumentsResponse{ReadTime: now}
		if i == 0 && len(transaction) > 0 {
			response.Transaction = transaction
		}
		document, err := s.loadDocument(name)
		if errors.Is(err, state.ErrFirestoreDocumentNotFound) {
			response.Result = &firestorepb.BatchGetDocumentsResponse_Missing{Missing: name}
		} else if err != nil {
			return status.Error(codes.Internal, err.Error())
		} else {
			response.Result = &firestorepb.BatchGetDocumentsResponse_Found{Found: applyDocumentMask(document, request.GetMask())}
		}
		if err := stream.Send(response); err != nil {
			return err
		}
	}
	return nil
}

func (s *firestoreServer) BeginTransaction(_ context.Context, _ *firestorepb.BeginTransactionRequest) (*firestorepb.BeginTransactionResponse, error) {
	return &firestorepb.BeginTransactionResponse{Transaction: []byte(newTransactionID())}, nil
}

func (s *firestoreServer) Rollback(_ context.Context, _ *firestorepb.RollbackRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (s *firestoreServer) Commit(_ context.Context, request *firestorepb.CommitRequest) (*firestorepb.CommitResponse, error) {
	response := &firestorepb.CommitResponse{}
	err := s.store.MutateFirestore(func(documents map[string]*state.FirestoreDocument, now time.Time) error {
		response.CommitTime = timestamppb.New(now)
		for _, write := range request.GetWrites() {
			_, result, err := applyFirestoreWrite(documents, write, now)
			if err != nil {
				return err
			}
			response.WriteResults = append(response.WriteResults, result)
		}
		return nil
	})
	if err != nil {
		return nil, normalizeFirestoreError(err)
	}
	return response, nil
}

func (s *firestoreServer) BatchWrite(_ context.Context, request *firestorepb.BatchWriteRequest) (*firestorepb.BatchWriteResponse, error) {
	response := &firestorepb.BatchWriteResponse{}
	err := s.store.MutateFirestore(func(documents map[string]*state.FirestoreDocument, now time.Time) error {
		for _, write := range request.GetWrites() {
			_, result, err := applyFirestoreWrite(documents, write, now)
			if err != nil {
				response.WriteResults = append(response.WriteResults, &firestorepb.WriteResult{})
				response.Status = append(response.Status, status.Convert(normalizeFirestoreError(err)).Proto())
				continue
			}
			response.WriteResults = append(response.WriteResults, result)
			response.Status = append(response.Status, status.New(codes.OK, "").Proto())
		}
		return nil
	})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return response, nil
}

func (s *firestoreServer) RunQuery(request *firestorepb.RunQueryRequest, stream firestorepb.Firestore_RunQueryServer) error {
	query := request.GetStructuredQuery()
	if query == nil || len(query.GetFrom()) == 0 {
		return status.Error(codes.InvalidArgument, "structured query with a collection selector is required")
	}
	selector := query.GetFrom()[0]
	prefix := strings.TrimSuffix(request.GetParent(), "/") + "/" + selector.GetCollectionId() + "/"
	storedDocuments := s.store.ListFirestoreDocuments(prefix)
	documents := make([]*firestorepb.Document, 0, len(storedDocuments))
	for _, stored := range storedDocuments {
		relative := strings.TrimPrefix(stored.Name, prefix)
		if !selector.GetAllDescendants() && strings.Contains(relative, "/") {
			continue
		}
		document, err := decodeStoredDocument(stored)
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		if matchesFirestoreFilter(document, query.GetWhere()) {
			documents = append(documents, document)
		}
	}
	sortFirestoreDocuments(documents, query.GetOrderBy())
	documents = applyFirestoreCursor(documents, query.GetOrderBy(), query.GetStartAt(), true)
	documents = applyFirestoreCursor(documents, query.GetOrderBy(), query.GetEndAt(), false)
	if offset := int(query.GetOffset()); offset > 0 {
		if offset >= len(documents) {
			documents = nil
		} else {
			documents = documents[offset:]
		}
	}
	if query.GetLimit() != nil && int(query.GetLimit().GetValue()) < len(documents) {
		documents = documents[:query.GetLimit().GetValue()]
	}
	readTime := timestamppb.Now()
	transaction := request.GetTransaction()
	if request.GetNewTransaction() != nil {
		transaction = []byte(newTransactionID())
	}
	for i, document := range documents {
		response := &firestorepb.RunQueryResponse{
			Document: applyProjection(document, query.GetSelect()),
			ReadTime: readTime,
		}
		if i == 0 && len(transaction) > 0 {
			response.Transaction = transaction
		}
		if err := stream.Send(response); err != nil {
			return err
		}
	}
	if len(documents) == 0 {
		return stream.Send(&firestorepb.RunQueryResponse{ReadTime: readTime, Transaction: transaction})
	}
	return nil
}

func (s *firestoreServer) loadDocument(name string) (*firestorepb.Document, error) {
	stored, err := s.store.FirestoreDocument(name)
	if err != nil {
		return nil, err
	}
	return decodeStoredDocument(stored)
}

func decodeStoredDocument(stored state.FirestoreDocument) (*firestorepb.Document, error) {
	document := &firestorepb.Document{}
	if err := proto.Unmarshal(stored.Proto, document); err != nil {
		return nil, err
	}
	document.CreateTime = timestamppb.New(stored.CreateTime)
	document.UpdateTime = timestamppb.New(stored.UpdateTime)
	return document, nil
}

func applyFirestoreWrite(documents map[string]*state.FirestoreDocument, write *firestorepb.Write, now time.Time) (*firestorepb.Document, *firestorepb.WriteResult, error) {
	switch operation := write.GetOperation().(type) {
	case *firestorepb.Write_Update:
		name := operation.Update.GetName()
		existing := documents[name]
		if err := checkFirestorePrecondition(existing, write.GetCurrentDocument()); err != nil {
			return nil, nil, err
		}
		var document *firestorepb.Document
		createdAt := now
		if existing != nil {
			decoded, err := decodeStoredDocument(*existing)
			if err != nil {
				return nil, nil, err
			}
			document = decoded
			createdAt = existing.CreateTime
		} else {
			document = &firestorepb.Document{Name: name, Fields: map[string]*firestorepb.Value{}}
		}
		if write.GetUpdateMask() == nil {
			document.Fields = cloneFirestoreFields(operation.Update.GetFields())
		} else {
			for _, path := range write.GetUpdateMask().GetFieldPaths() {
				value, ok := firestoreField(operation.Update, path)
				if ok {
					setFirestoreField(document, path, proto.Clone(value).(*firestorepb.Value))
				} else {
					deleteFirestoreField(document, path)
				}
			}
		}
		transformResults, err := applyFirestoreTransforms(document, write.GetUpdateTransforms(), now)
		if err != nil {
			return nil, nil, err
		}
		document.Name = name
		document.CreateTime = timestamppb.New(createdAt)
		document.UpdateTime = timestamppb.New(now)
		raw, err := proto.Marshal(document)
		if err != nil {
			return nil, nil, err
		}
		documents[name] = &state.FirestoreDocument{Name: name, Proto: raw, CreateTime: createdAt, UpdateTime: now}
		return document, &firestorepb.WriteResult{UpdateTime: timestamppb.New(now), TransformResults: transformResults}, nil
	case *firestorepb.Write_Delete:
		existing := documents[operation.Delete]
		if err := checkFirestorePrecondition(existing, write.GetCurrentDocument()); err != nil {
			return nil, nil, err
		}
		delete(documents, operation.Delete)
		return nil, &firestorepb.WriteResult{UpdateTime: timestamppb.New(now)}, nil
	case *firestorepb.Write_Transform:
		name := operation.Transform.GetDocument()
		existing := documents[name]
		if err := checkFirestorePrecondition(existing, write.GetCurrentDocument()); err != nil {
			return nil, nil, err
		}
		if existing == nil {
			return nil, nil, status.Error(codes.NotFound, "document not found")
		}
		document, err := decodeStoredDocument(*existing)
		if err != nil {
			return nil, nil, err
		}
		results, err := applyFirestoreTransforms(document, operation.Transform.GetFieldTransforms(), now)
		if err != nil {
			return nil, nil, err
		}
		document.UpdateTime = timestamppb.New(now)
		raw, err := proto.Marshal(document)
		if err != nil {
			return nil, nil, err
		}
		documents[name] = &state.FirestoreDocument{Name: name, Proto: raw, CreateTime: existing.CreateTime, UpdateTime: now}
		return document, &firestorepb.WriteResult{UpdateTime: timestamppb.New(now), TransformResults: results}, nil
	default:
		return nil, nil, status.Error(codes.InvalidArgument, "write operation is required")
	}
}

func checkFirestorePrecondition(existing *state.FirestoreDocument, precondition *firestorepb.Precondition) error {
	if precondition == nil || precondition.GetConditionType() == nil {
		return nil
	}
	switch condition := precondition.GetConditionType().(type) {
	case *firestorepb.Precondition_Exists:
		if condition.Exists != (existing != nil) {
			return status.Error(codes.FailedPrecondition, "document existence precondition failed")
		}
	case *firestorepb.Precondition_UpdateTime:
		if existing == nil || !existing.UpdateTime.Equal(condition.UpdateTime.AsTime()) {
			return status.Error(codes.FailedPrecondition, "document update time precondition failed")
		}
	}
	return nil
}

func applyFirestoreTransforms(document *firestorepb.Document, transforms []*firestorepb.DocumentTransform_FieldTransform, now time.Time) ([]*firestorepb.Value, error) {
	results := make([]*firestorepb.Value, 0, len(transforms))
	for _, transform := range transforms {
		var result *firestorepb.Value
		switch transform.GetTransformType().(type) {
		case *firestorepb.DocumentTransform_FieldTransform_SetToServerValue:
			result = &firestorepb.Value{ValueType: &firestorepb.Value_TimestampValue{TimestampValue: timestamppb.New(now)}}
		case *firestorepb.DocumentTransform_FieldTransform_Increment:
			current, _ := firestoreField(document, transform.GetFieldPath())
			result = incrementFirestoreValue(current, transform.GetIncrement())
		case *firestorepb.DocumentTransform_FieldTransform_Maximum:
			current, exists := firestoreField(document, transform.GetFieldPath())
			result = proto.Clone(transform.GetMaximum()).(*firestorepb.Value)
			if exists && compareFirestoreValues(current, result) >= 0 {
				result = proto.Clone(current).(*firestorepb.Value)
			}
		case *firestorepb.DocumentTransform_FieldTransform_Minimum:
			current, exists := firestoreField(document, transform.GetFieldPath())
			result = proto.Clone(transform.GetMinimum()).(*firestorepb.Value)
			if exists && compareFirestoreValues(current, result) <= 0 {
				result = proto.Clone(current).(*firestorepb.Value)
			}
		case *firestorepb.DocumentTransform_FieldTransform_AppendMissingElements:
			current, _ := firestoreField(document, transform.GetFieldPath())
			values := []*firestorepb.Value{}
			if current != nil && current.GetArrayValue() != nil {
				values = append(values, current.GetArrayValue().GetValues()...)
			}
			for _, candidate := range transform.GetAppendMissingElements().GetValues() {
				found := false
				for _, value := range values {
					if proto.Equal(value, candidate) {
						found = true
						break
					}
				}
				if !found {
					values = append(values, proto.Clone(candidate).(*firestorepb.Value))
				}
			}
			result = &firestorepb.Value{ValueType: &firestorepb.Value_ArrayValue{ArrayValue: &firestorepb.ArrayValue{Values: values}}}
		case *firestorepb.DocumentTransform_FieldTransform_RemoveAllFromArray:
			current, _ := firestoreField(document, transform.GetFieldPath())
			values := []*firestorepb.Value{}
			if current != nil && current.GetArrayValue() != nil {
				for _, value := range current.GetArrayValue().GetValues() {
					remove := false
					for _, candidate := range transform.GetRemoveAllFromArray().GetValues() {
						if proto.Equal(value, candidate) {
							remove = true
							break
						}
					}
					if !remove {
						values = append(values, proto.Clone(value).(*firestorepb.Value))
					}
				}
			}
			result = &firestorepb.Value{ValueType: &firestorepb.Value_ArrayValue{ArrayValue: &firestorepb.ArrayValue{Values: values}}}
		default:
			return nil, status.Error(codes.InvalidArgument, "unsupported field transform")
		}
		setFirestoreField(document, transform.GetFieldPath(), result)
		results = append(results, proto.Clone(result).(*firestorepb.Value))
	}
	return results, nil
}

func incrementFirestoreValue(current, increment *firestorepb.Value) *firestorepb.Value {
	if increment == nil {
		return &firestorepb.Value{ValueType: &firestorepb.Value_IntegerValue{IntegerValue: 0}}
	}
	if current == nil {
		return proto.Clone(increment).(*firestorepb.Value)
	}
	_, currentIsDouble := current.GetValueType().(*firestorepb.Value_DoubleValue)
	_, incrementIsDouble := increment.GetValueType().(*firestorepb.Value_DoubleValue)
	if currentIsDouble || incrementIsDouble {
		return &firestorepb.Value{ValueType: &firestorepb.Value_DoubleValue{DoubleValue: firestoreNumber(current) + firestoreNumber(increment)}}
	}
	return &firestorepb.Value{ValueType: &firestorepb.Value_IntegerValue{IntegerValue: current.GetIntegerValue() + increment.GetIntegerValue()}}
}

func cloneFirestoreFields(fields map[string]*firestorepb.Value) map[string]*firestorepb.Value {
	cloned := make(map[string]*firestorepb.Value, len(fields))
	for key, value := range fields {
		cloned[key] = proto.Clone(value).(*firestorepb.Value)
	}
	return cloned
}

func firestoreField(document *firestorepb.Document, fieldPath string) (*firestorepb.Value, bool) {
	if fieldPath == "__name__" {
		return &firestorepb.Value{ValueType: &firestorepb.Value_ReferenceValue{ReferenceValue: document.GetName()}}, true
	}
	parts := strings.Split(fieldPath, ".")
	fields := document.GetFields()
	for i, part := range parts {
		value, ok := fields[part]
		if !ok {
			return nil, false
		}
		if i == len(parts)-1 {
			return value, true
		}
		if value.GetMapValue() == nil {
			return nil, false
		}
		fields = value.GetMapValue().GetFields()
	}
	return nil, false
}

func setFirestoreField(document *firestorepb.Document, fieldPath string, value *firestorepb.Value) {
	if document.Fields == nil {
		document.Fields = map[string]*firestorepb.Value{}
	}
	parts := strings.Split(fieldPath, ".")
	fields := document.Fields
	for _, part := range parts[:len(parts)-1] {
		container, ok := fields[part]
		if !ok || container.GetMapValue() == nil {
			container = &firestorepb.Value{ValueType: &firestorepb.Value_MapValue{MapValue: &firestorepb.MapValue{Fields: map[string]*firestorepb.Value{}}}}
			fields[part] = container
		}
		fields = container.GetMapValue().Fields
	}
	fields[parts[len(parts)-1]] = value
}

func deleteFirestoreField(document *firestorepb.Document, fieldPath string) {
	parts := strings.Split(fieldPath, ".")
	fields := document.GetFields()
	for _, part := range parts[:len(parts)-1] {
		value, ok := fields[part]
		if !ok || value.GetMapValue() == nil {
			return
		}
		fields = value.GetMapValue().Fields
	}
	delete(fields, parts[len(parts)-1])
}

func applyDocumentMask(document *firestorepb.Document, mask *firestorepb.DocumentMask) *firestorepb.Document {
	if document == nil || mask == nil || len(mask.GetFieldPaths()) == 0 {
		return document
	}
	masked := &firestorepb.Document{Name: document.GetName(), CreateTime: document.GetCreateTime(), UpdateTime: document.GetUpdateTime(), Fields: map[string]*firestorepb.Value{}}
	for _, path := range mask.GetFieldPaths() {
		if value, ok := firestoreField(document, path); ok {
			setFirestoreField(masked, path, proto.Clone(value).(*firestorepb.Value))
		}
	}
	return masked
}

func applyProjection(document *firestorepb.Document, projection *firestorepb.StructuredQuery_Projection) *firestorepb.Document {
	if projection == nil {
		return document
	}
	mask := &firestorepb.DocumentMask{}
	for _, field := range projection.GetFields() {
		mask.FieldPaths = append(mask.FieldPaths, field.GetFieldPath())
	}
	return applyDocumentMask(document, mask)
}

func matchesFirestoreFilter(document *firestorepb.Document, filter *firestorepb.StructuredQuery_Filter) bool {
	if filter == nil {
		return true
	}
	if composite := filter.GetCompositeFilter(); composite != nil {
		if composite.GetOp() == firestorepb.StructuredQuery_CompositeFilter_OR {
			for _, nested := range composite.GetFilters() {
				if matchesFirestoreFilter(document, nested) {
					return true
				}
			}
			return false
		}
		for _, nested := range composite.GetFilters() {
			if !matchesFirestoreFilter(document, nested) {
				return false
			}
		}
		return true
	}
	if fieldFilter := filter.GetFieldFilter(); fieldFilter != nil {
		actual, exists := firestoreField(document, fieldFilter.GetField().GetFieldPath())
		if !exists {
			return false
		}
		expected := fieldFilter.GetValue()
		comparison := compareFirestoreValues(actual, expected)
		switch fieldFilter.GetOp() {
		case firestorepb.StructuredQuery_FieldFilter_EQUAL:
			return proto.Equal(actual, expected)
		case firestorepb.StructuredQuery_FieldFilter_NOT_EQUAL:
			return !proto.Equal(actual, expected)
		case firestorepb.StructuredQuery_FieldFilter_LESS_THAN:
			return comparison < 0
		case firestorepb.StructuredQuery_FieldFilter_LESS_THAN_OR_EQUAL:
			return comparison <= 0
		case firestorepb.StructuredQuery_FieldFilter_GREATER_THAN:
			return comparison > 0
		case firestorepb.StructuredQuery_FieldFilter_GREATER_THAN_OR_EQUAL:
			return comparison >= 0
		case firestorepb.StructuredQuery_FieldFilter_ARRAY_CONTAINS:
			for _, value := range actual.GetArrayValue().GetValues() {
				if proto.Equal(value, expected) {
					return true
				}
			}
			return false
		case firestorepb.StructuredQuery_FieldFilter_IN:
			for _, value := range expected.GetArrayValue().GetValues() {
				if proto.Equal(actual, value) {
					return true
				}
			}
			return false
		default:
			return false
		}
	}
	if unary := filter.GetUnaryFilter(); unary != nil {
		value, exists := firestoreField(document, unary.GetField().GetFieldPath())
		switch unary.GetOp() {
		case firestorepb.StructuredQuery_UnaryFilter_IS_NULL:
			return exists && value.GetNullValue() == structpb.NullValue_NULL_VALUE
		case firestorepb.StructuredQuery_UnaryFilter_IS_NOT_NULL:
			return exists && value.GetValueType() != nil && value.GetNullValue() != structpb.NullValue_NULL_VALUE
		default:
			return false
		}
	}
	return true
}

func sortFirestoreDocuments(documents []*firestorepb.Document, orders []*firestorepb.StructuredQuery_Order) {
	if len(orders) == 0 {
		orders = []*firestorepb.StructuredQuery_Order{{Field: &firestorepb.StructuredQuery_FieldReference{FieldPath: "__name__"}, Direction: firestorepb.StructuredQuery_ASCENDING}}
	}
	sort.SliceStable(documents, func(i, j int) bool {
		for _, order := range orders {
			left, _ := firestoreField(documents[i], order.GetField().GetFieldPath())
			right, _ := firestoreField(documents[j], order.GetField().GetFieldPath())
			comparison := compareFirestoreValues(left, right)
			if comparison == 0 {
				continue
			}
			if order.GetDirection() == firestorepb.StructuredQuery_DESCENDING {
				return comparison > 0
			}
			return comparison < 0
		}
		return documents[i].GetName() < documents[j].GetName()
	})
}

func applyFirestoreCursor(documents []*firestorepb.Document, orders []*firestorepb.StructuredQuery_Order, cursor *firestorepb.Cursor, start bool) []*firestorepb.Document {
	if cursor == nil || len(cursor.GetValues()) == 0 {
		return documents
	}
	if len(orders) == 0 {
		orders = []*firestorepb.StructuredQuery_Order{{Field: &firestorepb.StructuredQuery_FieldReference{FieldPath: "__name__"}, Direction: firestorepb.StructuredQuery_ASCENDING}}
	}
	keep := func(document *firestorepb.Document) bool {
		comparison := 0
		for i, expected := range cursor.GetValues() {
			if i >= len(orders) {
				break
			}
			actual, _ := firestoreField(document, orders[i].GetField().GetFieldPath())
			comparison = compareFirestoreValues(actual, expected)
			if orders[i].GetDirection() == firestorepb.StructuredQuery_DESCENDING {
				comparison = -comparison
			}
			if comparison != 0 {
				break
			}
		}
		if start {
			return comparison > 0 || (comparison == 0 && cursor.GetBefore())
		}
		return comparison < 0 || (comparison == 0 && cursor.GetBefore())
	}
	result := make([]*firestorepb.Document, 0, len(documents))
	for _, document := range documents {
		if keep(document) {
			result = append(result, document)
		}
	}
	return result
}

func compareFirestoreValues(left, right *firestorepb.Value) int {
	if left == nil && right == nil {
		return 0
	}
	if left == nil {
		return -1
	}
	if right == nil {
		return 1
	}
	if isFirestoreNumber(left) && isFirestoreNumber(right) {
		return compareOrdered(firestoreNumber(left), firestoreNumber(right))
	}
	switch left.GetValueType().(type) {
	case *firestorepb.Value_StringValue:
		return strings.Compare(left.GetStringValue(), right.GetStringValue())
	case *firestorepb.Value_ReferenceValue:
		return strings.Compare(left.GetReferenceValue(), right.GetReferenceValue())
	case *firestorepb.Value_TimestampValue:
		if left.GetTimestampValue().AsTime().Before(right.GetTimestampValue().AsTime()) {
			return -1
		}
		if left.GetTimestampValue().AsTime().After(right.GetTimestampValue().AsTime()) {
			return 1
		}
		return 0
	case *firestorepb.Value_BooleanValue:
		return compareOrdered(fmt.Sprint(left.GetBooleanValue()), fmt.Sprint(right.GetBooleanValue()))
	default:
		return strings.Compare(left.String(), right.String())
	}
}

func isFirestoreNumber(value *firestorepb.Value) bool {
	switch value.GetValueType().(type) {
	case *firestorepb.Value_IntegerValue, *firestorepb.Value_DoubleValue:
		return true
	default:
		return false
	}
}

func firestoreNumber(value *firestorepb.Value) float64 {
	if _, ok := value.GetValueType().(*firestorepb.Value_DoubleValue); ok {
		return value.GetDoubleValue()
	}
	return float64(value.GetIntegerValue())
}

func compareOrdered[T ~float64 | ~string](left, right T) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func normalizeFirestoreError(err error) error {
	if status.Code(err) != codes.Unknown {
		return err
	}
	if errors.Is(err, state.ErrFirestoreDocumentNotFound) {
		return status.Error(codes.NotFound, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}

func newTransactionID() string {
	return "fcp-tx-" + requestID()
}
