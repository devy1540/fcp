package server

import (
	"errors"
	"testing"
	"time"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"github.com/devy1540/fcp/internal/state"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestFirestoreFieldMasksAndNestedFields(t *testing.T) {
	document := &firestorepb.Document{
		Name: "projects/test/databases/(default)/documents/users/one",
		Fields: map[string]*firestorepb.Value{
			"name": firestoreStringValue("Podo"),
			"profile": {
				ValueType: &firestorepb.Value_MapValue{MapValue: &firestorepb.MapValue{Fields: map[string]*firestorepb.Value{
					"level": firestoreIntegerValue(1),
				}}},
			},
		},
	}

	if value, ok := firestoreField(document, "__name__"); !ok || value.GetReferenceValue() != document.Name {
		t.Fatalf("unexpected document name field: value=%v ok=%v", value, ok)
	}
	if value, ok := firestoreField(document, "profile.level"); !ok || value.GetIntegerValue() != 1 {
		t.Fatalf("unexpected nested field: value=%v ok=%v", value, ok)
	}
	if _, ok := firestoreField(document, "profile.missing"); ok {
		t.Fatal("missing nested field should not exist")
	}
	if _, ok := firestoreField(document, "name.value"); ok {
		t.Fatal("scalar field must not be traversable")
	}

	setFirestoreField(document, "profile.rank.name", firestoreStringValue("gold"))
	setFirestoreField(document, "settings.theme", firestoreStringValue("dark"))
	if value, ok := firestoreField(document, "profile.rank.name"); !ok || value.GetStringValue() != "gold" {
		t.Fatalf("nested field was not set: value=%v ok=%v", value, ok)
	}
	deleteFirestoreField(document, "profile.rank.name")
	deleteFirestoreField(document, "missing.path")
	if _, ok := firestoreField(document, "profile.rank.name"); ok {
		t.Fatal("nested field was not deleted")
	}

	masked := applyDocumentMask(document, &firestorepb.DocumentMask{FieldPaths: []string{"profile.level", "settings.theme", "missing"}})
	if len(masked.GetFields()) != 2 {
		t.Fatalf("unexpected masked fields: %+v", masked.GetFields())
	}
	if _, ok := firestoreField(masked, "name"); ok {
		t.Fatal("unmasked field should be omitted")
	}
	projected := applyProjection(document, &firestorepb.StructuredQuery_Projection{Fields: []*firestorepb.StructuredQuery_FieldReference{{FieldPath: "name"}}})
	if len(projected.GetFields()) != 1 || projected.GetFields()["name"].GetStringValue() != "Podo" {
		t.Fatalf("unexpected projection: %+v", projected.GetFields())
	}
	if applyDocumentMask(nil, &firestorepb.DocumentMask{FieldPaths: []string{"name"}}) != nil {
		t.Fatal("nil document mask should remain nil")
	}
	if applyDocumentMask(document, nil) != document {
		t.Fatal("nil mask should return the original document")
	}
}

func TestApplyFirestoreTransforms(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	document := &firestorepb.Document{Fields: map[string]*firestorepb.Value{
		"integer": firestoreIntegerValue(2),
		"double":  firestoreDoubleValue(1.5),
		"maximum": firestoreIntegerValue(9),
		"minimum": firestoreIntegerValue(1),
		"tags": firestoreArrayValue(
			firestoreStringValue("one"),
			firestoreStringValue("two"),
		),
	}}
	transforms := []*firestorepb.DocumentTransform_FieldTransform{
		{
			FieldPath: "updatedAt",
			TransformType: &firestorepb.DocumentTransform_FieldTransform_SetToServerValue{
				SetToServerValue: firestorepb.DocumentTransform_FieldTransform_REQUEST_TIME,
			},
		},
		{
			FieldPath:     "integer",
			TransformType: &firestorepb.DocumentTransform_FieldTransform_Increment{Increment: firestoreIntegerValue(3)},
		},
		{
			FieldPath:     "double",
			TransformType: &firestorepb.DocumentTransform_FieldTransform_Increment{Increment: firestoreDoubleValue(0.5)},
		},
		{
			FieldPath:     "newCounter",
			TransformType: &firestorepb.DocumentTransform_FieldTransform_Increment{Increment: firestoreIntegerValue(4)},
		},
		{
			FieldPath:     "zeroCounter",
			TransformType: &firestorepb.DocumentTransform_FieldTransform_Increment{},
		},
		{
			FieldPath:     "maximum",
			TransformType: &firestorepb.DocumentTransform_FieldTransform_Maximum{Maximum: firestoreIntegerValue(4)},
		},
		{
			FieldPath:     "minimum",
			TransformType: &firestorepb.DocumentTransform_FieldTransform_Minimum{Minimum: firestoreIntegerValue(4)},
		},
		{
			FieldPath: "tags",
			TransformType: &firestorepb.DocumentTransform_FieldTransform_AppendMissingElements{
				AppendMissingElements: &firestorepb.ArrayValue{Values: []*firestorepb.Value{firestoreStringValue("two"), firestoreStringValue("three")}},
			},
		},
		{
			FieldPath: "tags",
			TransformType: &firestorepb.DocumentTransform_FieldTransform_RemoveAllFromArray{
				RemoveAllFromArray: &firestorepb.ArrayValue{Values: []*firestorepb.Value{firestoreStringValue("one")}},
			},
		},
	}

	results, err := applyFirestoreTransforms(document, transforms, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(transforms) {
		t.Fatalf("unexpected transform result count: %d", len(results))
	}
	if got := document.GetFields()["updatedAt"].GetTimestampValue().AsTime(); !got.Equal(now) {
		t.Fatalf("unexpected server timestamp: %v", got)
	}
	if got := document.GetFields()["integer"].GetIntegerValue(); got != 5 {
		t.Fatalf("unexpected integer increment: %d", got)
	}
	if got := document.GetFields()["double"].GetDoubleValue(); got != 2 {
		t.Fatalf("unexpected double increment: %f", got)
	}
	if got := document.GetFields()["newCounter"].GetIntegerValue(); got != 4 {
		t.Fatalf("unexpected missing-field increment: %d", got)
	}
	if got := document.GetFields()["zeroCounter"].GetIntegerValue(); got != 0 {
		t.Fatalf("unexpected nil increment: %d", got)
	}
	if got := document.GetFields()["maximum"].GetIntegerValue(); got != 9 {
		t.Fatalf("maximum should preserve the larger current value: %d", got)
	}
	if got := document.GetFields()["minimum"].GetIntegerValue(); got != 1 {
		t.Fatalf("minimum should preserve the smaller current value: %d", got)
	}
	tags := document.GetFields()["tags"].GetArrayValue().GetValues()
	if len(tags) != 2 || tags[0].GetStringValue() != "two" || tags[1].GetStringValue() != "three" {
		t.Fatalf("unexpected array transforms: %+v", tags)
	}

	_, err = applyFirestoreTransforms(document, []*firestorepb.DocumentTransform_FieldTransform{{FieldPath: "bad"}}, now)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unsupported transform should fail with InvalidArgument: %v", err)
	}
}

func TestApplyFirestoreWriteLifecycleAndPreconditions(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	name := "projects/test/databases/(default)/documents/users/one"
	documents := map[string]*state.FirestoreDocument{}

	created, result, err := applyFirestoreWrite(documents, &firestorepb.Write{
		Operation: &firestorepb.Write_Update{Update: &firestorepb.Document{
			Name: name,
			Fields: map[string]*firestorepb.Value{
				"name":     firestoreStringValue("Podo"),
				"obsolete": firestoreStringValue("remove"),
			},
		}},
		CurrentDocument: &firestorepb.Precondition{ConditionType: &firestorepb.Precondition_Exists{Exists: false}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if created.GetCreateTime().AsTime() != now || result.GetUpdateTime().AsTime() != now {
		t.Fatalf("unexpected create result: document=%v result=%v", created, result)
	}

	next := now.Add(time.Minute)
	updated, _, err := applyFirestoreWrite(documents, &firestorepb.Write{
		Operation: &firestorepb.Write_Update{Update: &firestorepb.Document{
			Name: name,
			Fields: map[string]*firestorepb.Value{
				"profile": {
					ValueType: &firestorepb.Value_MapValue{MapValue: &firestorepb.MapValue{Fields: map[string]*firestorepb.Value{
						"level": firestoreIntegerValue(2),
					}}},
				},
			},
		}},
		UpdateMask: &firestorepb.DocumentMask{FieldPaths: []string{"profile.level", "obsolete"}},
		CurrentDocument: &firestorepb.Precondition{ConditionType: &firestorepb.Precondition_UpdateTime{
			UpdateTime: timestamppb.New(now),
		}},
	}, next)
	if err != nil {
		t.Fatal(err)
	}
	if updated.GetCreateTime().AsTime() != now {
		t.Fatalf("create time should be preserved: %v", updated.GetCreateTime())
	}
	if _, ok := updated.GetFields()["obsolete"]; ok {
		t.Fatal("masked absent field should be deleted")
	}
	if value, ok := firestoreField(updated, "profile.level"); !ok || value.GetIntegerValue() != 2 {
		t.Fatalf("masked nested field should be updated: value=%v ok=%v", value, ok)
	}

	transformed, transformResult, err := applyFirestoreWrite(documents, &firestorepb.Write{
		Operation: &firestorepb.Write_Transform{Transform: &firestorepb.DocumentTransform{
			Document: name,
			FieldTransforms: []*firestorepb.DocumentTransform_FieldTransform{{
				FieldPath:     "visits",
				TransformType: &firestorepb.DocumentTransform_FieldTransform_Increment{Increment: firestoreIntegerValue(1)},
			}},
		}},
	}, next.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if transformed.GetFields()["visits"].GetIntegerValue() != 1 || len(transformResult.GetTransformResults()) != 1 {
		t.Fatalf("unexpected transform write: document=%v result=%v", transformed, transformResult)
	}

	_, _, err = applyFirestoreWrite(documents, &firestorepb.Write{
		Operation:       &firestorepb.Write_Update{Update: &firestorepb.Document{Name: name}},
		CurrentDocument: &firestorepb.Precondition{ConditionType: &firestorepb.Precondition_Exists{Exists: false}},
	}, next)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("existence precondition should fail: %v", err)
	}
	_, _, err = applyFirestoreWrite(documents, &firestorepb.Write{
		Operation: &firestorepb.Write_Transform{Transform: &firestorepb.DocumentTransform{Document: name + "-missing"}},
	}, next)
	if status.Code(err) != codes.NotFound {
		t.Fatalf("missing transform target should fail: %v", err)
	}
	_, _, err = applyFirestoreWrite(documents, &firestorepb.Write{}, next)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty write should fail: %v", err)
	}
	_, _, err = applyFirestoreWrite(documents, &firestorepb.Write{
		Operation: &firestorepb.Write_Delete{Delete: name},
		CurrentDocument: &firestorepb.Precondition{ConditionType: &firestorepb.Precondition_UpdateTime{
			UpdateTime: timestamppb.New(now),
		}},
	}, next)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("stale update-time precondition should fail: %v", err)
	}
	_, _, err = applyFirestoreWrite(documents, &firestorepb.Write{
		Operation: &firestorepb.Write_Delete{Delete: name},
	}, next)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := documents[name]; exists {
		t.Fatal("document was not deleted")
	}
}

func TestFirestoreFiltersOrderingAndCursors(t *testing.T) {
	documents := []*firestorepb.Document{
		{Name: "users/two", Fields: map[string]*firestorepb.Value{
			"score": firestoreIntegerValue(2),
			"name":  firestoreStringValue("beta"),
			"tags":  firestoreArrayValue(firestoreStringValue("blue")),
			"empty": {ValueType: &firestorepb.Value_NullValue{NullValue: structpb.NullValue_NULL_VALUE}},
		}},
		{Name: "users/one", Fields: map[string]*firestorepb.Value{
			"score": firestoreDoubleValue(1),
			"name":  firestoreStringValue("alpha"),
			"tags":  firestoreArrayValue(firestoreStringValue("red"), firestoreStringValue("blue")),
		}},
		{Name: "users/three", Fields: map[string]*firestorepb.Value{
			"score": firestoreIntegerValue(3),
			"name":  firestoreStringValue("gamma"),
		}},
	}

	fieldFilter := func(field string, op firestorepb.StructuredQuery_FieldFilter_Operator, value *firestorepb.Value) *firestorepb.StructuredQuery_Filter {
		return &firestorepb.StructuredQuery_Filter{FilterType: &firestorepb.StructuredQuery_Filter_FieldFilter{
			FieldFilter: &firestorepb.StructuredQuery_FieldFilter{
				Field: &firestorepb.StructuredQuery_FieldReference{FieldPath: field},
				Op:    op,
				Value: value,
			},
		}}
	}
	if !matchesFirestoreFilter(documents[0], fieldFilter("score", firestorepb.StructuredQuery_FieldFilter_GREATER_THAN, firestoreIntegerValue(1))) {
		t.Fatal("greater-than filter should match")
	}
	if !matchesFirestoreFilter(documents[0], fieldFilter("name", firestorepb.StructuredQuery_FieldFilter_EQUAL, firestoreStringValue("beta"))) {
		t.Fatal("equal filter should match")
	}
	if !matchesFirestoreFilter(documents[0], fieldFilter("name", firestorepb.StructuredQuery_FieldFilter_NOT_EQUAL, firestoreStringValue("alpha"))) {
		t.Fatal("not-equal filter should match")
	}
	if !matchesFirestoreFilter(documents[0], fieldFilter("tags", firestorepb.StructuredQuery_FieldFilter_ARRAY_CONTAINS, firestoreStringValue("blue"))) {
		t.Fatal("array-contains filter should match")
	}
	if !matchesFirestoreFilter(documents[0], fieldFilter("name", firestorepb.StructuredQuery_FieldFilter_IN, firestoreArrayValue(firestoreStringValue("alpha"), firestoreStringValue("beta")))) {
		t.Fatal("in filter should match")
	}
	if matchesFirestoreFilter(documents[0], fieldFilter("missing", firestorepb.StructuredQuery_FieldFilter_EQUAL, firestoreStringValue("x"))) {
		t.Fatal("missing field should not match")
	}
	if matchesFirestoreFilter(documents[0], fieldFilter("name", firestorepb.StructuredQuery_FieldFilter_OPERATOR_UNSPECIFIED, firestoreStringValue("beta"))) {
		t.Fatal("unsupported field operator should not match")
	}

	isNull := &firestorepb.StructuredQuery_Filter{FilterType: &firestorepb.StructuredQuery_Filter_UnaryFilter{
		UnaryFilter: &firestorepb.StructuredQuery_UnaryFilter{
			Op:          firestorepb.StructuredQuery_UnaryFilter_IS_NULL,
			OperandType: &firestorepb.StructuredQuery_UnaryFilter_Field{Field: &firestorepb.StructuredQuery_FieldReference{FieldPath: "empty"}},
		},
	}}
	if !matchesFirestoreFilter(documents[0], isNull) {
		t.Fatal("IS_NULL filter should match")
	}
	isNotNull := proto.Clone(isNull).(*firestorepb.StructuredQuery_Filter)
	isNotNull.GetUnaryFilter().Op = firestorepb.StructuredQuery_UnaryFilter_IS_NOT_NULL
	isNotNull.GetUnaryFilter().GetField().FieldPath = "name"
	if !matchesFirestoreFilter(documents[0], isNotNull) {
		t.Fatal("IS_NOT_NULL filter should match")
	}

	composite := &firestorepb.StructuredQuery_Filter{FilterType: &firestorepb.StructuredQuery_Filter_CompositeFilter{
		CompositeFilter: &firestorepb.StructuredQuery_CompositeFilter{
			Op: firestorepb.StructuredQuery_CompositeFilter_AND,
			Filters: []*firestorepb.StructuredQuery_Filter{
				fieldFilter("score", firestorepb.StructuredQuery_FieldFilter_GREATER_THAN_OR_EQUAL, firestoreIntegerValue(2)),
				fieldFilter("score", firestorepb.StructuredQuery_FieldFilter_LESS_THAN_OR_EQUAL, firestoreIntegerValue(2)),
			},
		},
	}}
	if !matchesFirestoreFilter(documents[0], composite) || matchesFirestoreFilter(documents[1], composite) {
		t.Fatal("AND composite filter returned an unexpected result")
	}
	composite.GetCompositeFilter().Op = firestorepb.StructuredQuery_CompositeFilter_OR
	if !matchesFirestoreFilter(documents[0], composite) {
		t.Fatal("OR composite filter should match")
	}
	if !matchesFirestoreFilter(documents[0], nil) {
		t.Fatal("nil filter should match")
	}

	orders := []*firestorepb.StructuredQuery_Order{{
		Field:     &firestorepb.StructuredQuery_FieldReference{FieldPath: "score"},
		Direction: firestorepb.StructuredQuery_DESCENDING,
	}}
	sortFirestoreDocuments(documents, orders)
	if documents[0].GetName() != "users/three" || documents[2].GetName() != "users/one" {
		t.Fatalf("unexpected descending order: %s %s %s", documents[0].GetName(), documents[1].GetName(), documents[2].GetName())
	}
	started := applyFirestoreCursor(documents, orders, &firestorepb.Cursor{Values: []*firestorepb.Value{firestoreIntegerValue(2)}}, true)
	if len(started) != 1 || started[0].GetName() != "users/one" {
		t.Fatalf("unexpected start cursor result: %+v", started)
	}
	ended := applyFirestoreCursor(documents, orders, &firestorepb.Cursor{Values: []*firestorepb.Value{firestoreIntegerValue(2)}, Before: true}, false)
	if len(ended) != 2 || ended[1].GetName() != "users/two" {
		t.Fatalf("unexpected end cursor result: %+v", ended)
	}
	if got := applyFirestoreCursor(documents, orders, nil, true); len(got) != len(documents) {
		t.Fatalf("nil cursor should preserve documents: %d", len(got))
	}

	sortFirestoreDocuments(documents, nil)
	if documents[0].GetName() != "users/one" {
		t.Fatalf("default ordering should use document name: %+v", documents)
	}
}

func TestFirestoreValueComparisonAndErrorNormalization(t *testing.T) {
	if compareFirestoreValues(nil, nil) != 0 || compareFirestoreValues(nil, firestoreIntegerValue(1)) >= 0 || compareFirestoreValues(firestoreIntegerValue(1), nil) <= 0 {
		t.Fatal("nil value ordering is incorrect")
	}
	if compareFirestoreValues(firestoreIntegerValue(2), firestoreDoubleValue(2.5)) >= 0 {
		t.Fatal("numeric comparison is incorrect")
	}
	if compareFirestoreValues(firestoreStringValue("a"), firestoreStringValue("b")) >= 0 {
		t.Fatal("string comparison is incorrect")
	}
	leftTime := time.Date(2026, 7, 27, 1, 0, 0, 0, time.UTC)
	rightTime := leftTime.Add(time.Second)
	if compareFirestoreValues(firestoreTimestampValue(leftTime), firestoreTimestampValue(rightTime)) >= 0 {
		t.Fatal("timestamp comparison is incorrect")
	}
	if compareFirestoreValues(firestoreBooleanValue(false), firestoreBooleanValue(true)) >= 0 {
		t.Fatal("boolean comparison is incorrect")
	}
	if compareOrdered(1.0, 1.0) != 0 || compareOrdered("b", "a") <= 0 {
		t.Fatal("generic ordered comparison is incorrect")
	}
	if !isFirestoreNumber(firestoreIntegerValue(1)) || isFirestoreNumber(firestoreStringValue("1")) {
		t.Fatal("numeric type detection is incorrect")
	}

	notFound := normalizeFirestoreError(state.ErrFirestoreDocumentNotFound)
	if status.Code(notFound) != codes.NotFound {
		t.Fatalf("state not-found error was not normalized: %v", notFound)
	}
	known := status.Error(codes.InvalidArgument, "bad")
	if normalizeFirestoreError(known) != known {
		t.Fatal("known gRPC status should be preserved")
	}
	internal := normalizeFirestoreError(errors.New("broken"))
	if status.Code(internal) != codes.Internal {
		t.Fatalf("unknown error should become Internal: %v", internal)
	}
}

func firestoreStringValue(value string) *firestorepb.Value {
	return &firestorepb.Value{ValueType: &firestorepb.Value_StringValue{StringValue: value}}
}

func firestoreIntegerValue(value int64) *firestorepb.Value {
	return &firestorepb.Value{ValueType: &firestorepb.Value_IntegerValue{IntegerValue: value}}
}

func firestoreDoubleValue(value float64) *firestorepb.Value {
	return &firestorepb.Value{ValueType: &firestorepb.Value_DoubleValue{DoubleValue: value}}
}

func firestoreBooleanValue(value bool) *firestorepb.Value {
	return &firestorepb.Value{ValueType: &firestorepb.Value_BooleanValue{BooleanValue: value}}
}

func firestoreTimestampValue(value time.Time) *firestorepb.Value {
	return &firestorepb.Value{ValueType: &firestorepb.Value_TimestampValue{TimestampValue: timestamppb.New(value)}}
}

func firestoreArrayValue(values ...*firestorepb.Value) *firestorepb.Value {
	return &firestorepb.Value{ValueType: &firestorepb.Value_ArrayValue{ArrayValue: &firestorepb.ArrayValue{Values: values}}}
}
