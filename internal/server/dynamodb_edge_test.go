package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devy1540/fcp/internal/state"
)

func TestDynamoConditionExpressionEdges(t *testing.T) {
	name, prefix, other := "alpha", "al", "beta"
	binary, binaryPrefix := "YWJj", "YW"
	one, two, three := "1", "2", "3"
	item := state.DynamoItem{
		"name":  {S: &name},
		"blob":  {B: &binary},
		"count": {N: &two},
	}
	values := map[string]state.DynamoAttributeValue{
		":prefix":       {S: &prefix},
		":other":        {S: &other},
		":binaryPrefix": {B: &binaryPrefix},
		":one":          {N: &one},
		":two":          {N: &two},
		":three":        {N: &three},
	}
	names := map[string]string{"#name": "name"}

	for _, test := range []struct {
		name       string
		expression string
		exists     bool
		want       bool
	}{
		{name: "exists", expression: "attribute_exists(#name)", exists: true, want: true},
		{name: "not exists missing", expression: "attribute_not_exists(missing)", exists: true, want: true},
		{name: "not exists item", expression: "attribute_not_exists(name)", exists: true, want: false},
		{name: "begins with string", expression: "begins_with(#name, :prefix)", exists: true, want: true},
		{name: "begins with binary", expression: "begins_with(blob, :binaryPrefix)", exists: true, want: true},
		{name: "between", expression: "count BETWEEN :one AND :three", exists: true, want: true},
		{name: "parentheses and conjunction", expression: "(count >= :two) AND name <> :other", exists: true, want: true},
		{name: "less", expression: "count < :three", exists: true, want: true},
		{name: "less equal", expression: "count <= :two", exists: true, want: true},
		{name: "greater", expression: "count > :one", exists: true, want: true},
		{name: "equal", expression: "count = :two", exists: true, want: true},
		{name: "missing attribute", expression: "missing = :two", exists: true, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			condition, err := compileDynamoCondition(test.expression, names, values)
			if err != nil {
				t.Fatal(err)
			}
			if got := condition(item, test.exists); got != test.want {
				t.Fatalf("condition result=%v want=%v", got, test.want)
			}
		})
	}

	for _, expression := range []string{
		"(",
		"attribute_exists name)",
		"attribute_exists()",
		"begins_with(name :prefix)",
		"begins_with(name, :missing)",
		"count BETWEEN :missing AND :three",
		"count BETWEEN :one :three",
		"count ? :two",
		"count = :missing",
		"count = :two trailing",
	} {
		if _, err := compileDynamoCondition(expression, names, values); !errors.Is(err, state.ErrDynamoValidation) {
			t.Fatalf("expression %q should fail validation: %v", expression, err)
		}
	}
	if condition, err := compileDynamoCondition("", nil, nil); err != nil || condition != nil {
		t.Fatalf("empty condition should be omitted: condition=%v err=%v", condition, err)
	}
	if dynamoBeginsWith(state.DynamoAttributeValue{N: &two}, state.DynamoAttributeValue{N: &one}) {
		t.Fatal("begins_with should reject numeric attributes")
	}
	if dynamoCompare(state.DynamoAttributeValue{N: ptrString("invalid")}, state.DynamoAttributeValue{N: &one}) != 1 {
		t.Fatal("invalid numbers should fall back to non-equal ordering")
	}
}

func TestApplyDynamoUpdateAndReturnValueEdges(t *testing.T) {
	one, two, text := "1", "2", "value"
	item := state.DynamoItem{
		"count": {N: &one},
		"keep":  {S: &text},
	}
	values := map[string]state.DynamoAttributeValue{
		":one":  {N: &one},
		":two":  {N: &two},
		":text": {S: &text},
	}
	updated, names, err := applyDynamoUpdate(item, "SET #new = if_not_exists(#new, :text), keep = if_not_exists(keep, :text) REMOVE missing ADD count :two", map[string]string{"#new": "new"}, values)
	if err != nil {
		t.Fatal(err)
	}
	if updated["new"].S == nil || *updated["new"].S != text || updated["count"].N == nil || *updated["count"].N != "3" {
		t.Fatalf("unexpected update result: %+v", updated)
	}
	if len(names) != 3 {
		t.Fatalf("unexpected updated names: %+v", names)
	}

	for _, test := range []struct {
		name       string
		expression string
		values     map[string]state.DynamoAttributeValue
	}{
		{name: "missing expression"},
		{name: "invalid set", expression: "SET name"},
		{name: "invalid if not exists", expression: "SET name = if_not_exists(name)", values: values},
		{name: "missing value", expression: "SET name = :missing", values: values},
		{name: "invalid add", expression: "ADD count", values: values},
		{name: "non numeric add", expression: "ADD count :text", values: values},
		{name: "non numeric target", expression: "ADD keep :one", values: values},
		{name: "unsupported delete", expression: "DELETE keep :text", values: values},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := applyDynamoUpdate(state.DynamoItem{"keep": {S: &text}}, test.expression, nil, test.values)
			if !errors.Is(err, state.ErrDynamoValidation) {
				t.Fatalf("expected validation error: %v", err)
			}
		})
	}
	if _, err := addDynamoNumbers("invalid", "1"); !errors.Is(err, state.ErrDynamoValidation) {
		t.Fatalf("invalid number should fail: %v", err)
	}
	if sum, err := addDynamoNumbers("1.25", "2.5"); err != nil || sum != "3.75" {
		t.Fatalf("unexpected decimal sum: %q err=%v", sum, err)
	}

	old := state.DynamoItem{"old": {S: &text}, "changed": {N: &one}}
	next := state.DynamoItem{"new": {S: &text}, "changed": {N: &two}}
	if got := dynamoReturnAttributes("ALL_OLD", old, next, nil); got["old"].S == nil {
		t.Fatalf("ALL_OLD did not return the old item: %+v", got)
	}
	if got := dynamoReturnAttributes("ALL_NEW", old, next, nil); got["new"].S == nil {
		t.Fatalf("ALL_NEW did not return the new item: %+v", got)
	}
	if got := dynamoReturnAttributes("UPDATED_OLD", old, next, []string{"changed"}); len(got) != 1 || got["changed"].N == nil {
		t.Fatalf("UPDATED_OLD returned unexpected attributes: %+v", got)
	}
	if got := dynamoReturnAttributes("UPDATED_NEW", old, next, []string{"changed"}); len(got) != 1 || got["changed"].N == nil || *got["changed"].N != two {
		t.Fatalf("UPDATED_NEW returned unexpected attributes: %+v", got)
	}
	if got := dynamoReturnAttributes("NONE", old, next, nil); got != nil {
		t.Fatalf("NONE should not return attributes: %+v", got)
	}
}

func TestDynamoProtocolValidationAndErrorMapping(t *testing.T) {
	server := newTestServer(t)
	assertHTTPStatus(t, http.MethodGet, server.URL+"/", nil, map[string]string{"X-Amz-Target": dynamoTargetPrefix + "ListTables"}, http.StatusMethodNotAllowed, "")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/", strings.NewReader(`{}`), map[string]string{"X-Amz-Target": dynamoTargetPrefix}, http.StatusBadRequest, "UnknownOperationException")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/", strings.NewReader(`{`), map[string]string{"X-Amz-Target": dynamoTargetPrefix + "CreateTable"}, http.StatusBadRequest, "SerializationException")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/", strings.NewReader(`{}`), map[string]string{"X-Amz-Target": dynamoTargetPrefix + "Unknown"}, http.StatusBadRequest, "UnknownOperationException")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/", strings.NewReader(`{"RequestItems":{}}`), map[string]string{"X-Amz-Target": dynamoTargetPrefix + "BatchGetItem"}, http.StatusBadRequest, "ValidationException")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/", strings.NewReader(`{"RequestItems":{}}`), map[string]string{"X-Amz-Target": dynamoTargetPrefix + "BatchWriteItem"}, http.StatusBadRequest, "ValidationException")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/", strings.NewReader(`{"TransactItems":[]}`), map[string]string{"X-Amz-Target": dynamoTargetPrefix + "TransactWriteItems"}, http.StatusBadRequest, "ValidationException")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/", strings.NewReader(`{"TransactItems":[{}]}`), map[string]string{"X-Amz-Target": dynamoTargetPrefix + "TransactWriteItems"}, http.StatusBadRequest, "ValidationException")

	for _, test := range []struct {
		err  error
		code string
	}{
		{err: state.ErrDynamoTableNotFound, code: "ResourceNotFoundException"},
		{err: state.ErrDynamoTableExists, code: "ResourceInUseException"},
		{err: state.ErrDynamoConditionalCheckFailed, code: "ConditionalCheckFailedException"},
		{err: state.ErrDynamoValidation, code: "ValidationException"},
		{err: errors.New("broken"), code: "InternalServerError"},
	} {
		recorder := httptest.NewRecorder()
		writeDynamoStateError(recorder, test.err)
		if recorder.Code != http.StatusBadRequest || recorder.Header().Get("x-amzn-ErrorType") != test.code {
			t.Fatalf("error=%v status=%d code=%q", test.err, recorder.Code, recorder.Header().Get("x-amzn-ErrorType"))
		}
	}
}

func ptrString(value string) *string {
	return &value
}
