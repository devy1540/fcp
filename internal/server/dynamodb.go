package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"github.com/devy1540/fcp/internal/state"
)

const dynamoTargetPrefix = "DynamoDB_20120810."

type dynamoCreateTableRequest struct {
	TableName            string                            `json:"TableName"`
	KeySchema            []state.DynamoKeySchemaElement    `json:"KeySchema"`
	AttributeDefinitions []state.DynamoAttributeDefinition `json:"AttributeDefinitions"`
	BillingMode          string                            `json:"BillingMode"`
}

type dynamoTableRequest struct {
	TableName string `json:"TableName"`
}

type dynamoListTablesRequest struct {
	ExclusiveStartTableName string `json:"ExclusiveStartTableName"`
	Limit                   int    `json:"Limit"`
}

type dynamoItemRequest struct {
	TableName                 string                                `json:"TableName"`
	Item                      state.DynamoItem                      `json:"Item"`
	Key                       state.DynamoItem                      `json:"Key"`
	ConditionExpression       string                                `json:"ConditionExpression"`
	UpdateExpression          string                                `json:"UpdateExpression"`
	KeyConditionExpression    string                                `json:"KeyConditionExpression"`
	FilterExpression          string                                `json:"FilterExpression"`
	ProjectionExpression      string                                `json:"ProjectionExpression"`
	ExpressionAttributeNames  map[string]string                     `json:"ExpressionAttributeNames"`
	ExpressionAttributeValues map[string]state.DynamoAttributeValue `json:"ExpressionAttributeValues"`
	ExclusiveStartKey         state.DynamoItem                      `json:"ExclusiveStartKey"`
	Limit                     int                                   `json:"Limit"`
	ScanIndexForward          *bool                                 `json:"ScanIndexForward"`
	ReturnValues              string                                `json:"ReturnValues"`
	Select                    string                                `json:"Select"`
}

type dynamoTransactWriteRequest struct {
	TransactItems []struct {
		Put *struct {
			TableName string           `json:"TableName"`
			Item      state.DynamoItem `json:"Item"`
		} `json:"Put,omitempty"`
		Delete *struct {
			TableName string           `json:"TableName"`
			Key       state.DynamoItem `json:"Key"`
		} `json:"Delete,omitempty"`
	} `json:"TransactItems"`
}

type dynamoBatchGetRequest struct {
	RequestItems map[string]struct {
		Keys                     []state.DynamoItem `json:"Keys"`
		ProjectionExpression     string             `json:"ProjectionExpression"`
		ExpressionAttributeNames map[string]string  `json:"ExpressionAttributeNames"`
		AttributesToGet          []string           `json:"AttributesToGet"`
	} `json:"RequestItems"`
}

type dynamoBatchWriteRequest struct {
	RequestItems map[string][]struct {
		PutRequest *struct {
			Item state.DynamoItem `json:"Item"`
		} `json:"PutRequest,omitempty"`
		DeleteRequest *struct {
			Key state.DynamoItem `json:"Key"`
		} `json:"DeleteRequest,omitempty"`
	} `json:"RequestItems"`
}

func (s *Server) handleDynamoDB(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	target := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), dynamoTargetPrefix)
	if target == r.Header.Get("X-Amz-Target") || target == "" {
		writeDynamoError(w, "UnknownOperationException", "unsupported DynamoDB operation")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 20<<20))
	switch target {
	case "CreateTable":
		var request dynamoCreateTableRequest
		if !decodeDynamoRequest(w, decoder, &request) {
			return
		}
		table, err := s.store.CreateDynamoTable(request.TableName, request.KeySchema, request.AttributeDefinitions, request.BillingMode)
		if err != nil {
			writeDynamoStateError(w, err)
			return
		}
		writeDynamoJSON(w, map[string]any{"TableDescription": dynamoTableDescription(table)})
	case "DescribeTable":
		var request dynamoTableRequest
		if !decodeDynamoRequest(w, decoder, &request) {
			return
		}
		table, err := s.store.DynamoTable(request.TableName)
		if err != nil {
			writeDynamoStateError(w, err)
			return
		}
		writeDynamoJSON(w, map[string]any{"Table": dynamoTableDescription(table)})
	case "ListTables":
		var request dynamoListTablesRequest
		if !decodeDynamoRequest(w, decoder, &request) {
			return
		}
		s.handleDynamoListTables(w, request)
	case "DeleteTable":
		var request dynamoTableRequest
		if !decodeDynamoRequest(w, decoder, &request) {
			return
		}
		table, err := s.store.DynamoTable(request.TableName)
		if err == nil {
			err = s.store.DeleteDynamoTable(request.TableName)
		}
		if err != nil {
			writeDynamoStateError(w, err)
			return
		}
		description := dynamoTableDescription(table)
		description["TableStatus"] = "DELETING"
		writeDynamoJSON(w, map[string]any{"TableDescription": description})
	case "PutItem", "GetItem", "DeleteItem", "UpdateItem", "Query", "Scan":
		var request dynamoItemRequest
		if !decodeDynamoRequest(w, decoder, &request) {
			return
		}
		s.handleDynamoItemOperation(w, target, request)
	case "TransactWriteItems":
		var request dynamoTransactWriteRequest
		if !decodeDynamoRequest(w, decoder, &request) {
			return
		}
		s.handleDynamoTransactWrite(w, request)
	case "BatchGetItem":
		var request dynamoBatchGetRequest
		if !decodeDynamoRequest(w, decoder, &request) {
			return
		}
		s.handleDynamoBatchGet(w, request)
	case "BatchWriteItem":
		var request dynamoBatchWriteRequest
		if !decodeDynamoRequest(w, decoder, &request) {
			return
		}
		s.handleDynamoBatchWrite(w, request)
	default:
		writeDynamoError(w, "UnknownOperationException", "DynamoDB operation "+target+" is not implemented")
	}
}

func (s *Server) handleDynamoBatchGet(w http.ResponseWriter, request dynamoBatchGetRequest) {
	totalKeys := 0
	for _, tableRequest := range request.RequestItems {
		totalKeys += len(tableRequest.Keys)
	}
	if totalKeys == 0 || totalKeys > 100 {
		writeDynamoStateError(w, fmt.Errorf("%w: BatchGetItem must contain 1 to 100 keys", state.ErrDynamoValidation))
		return
	}
	responses := make(map[string][]state.DynamoItem, len(request.RequestItems))
	for tableName, tableRequest := range request.RequestItems {
		if _, err := s.store.DynamoTable(tableName); err != nil {
			writeDynamoStateError(w, err)
			return
		}
		projection := tableRequest.ProjectionExpression
		if projection == "" && len(tableRequest.AttributesToGet) > 0 {
			projection = strings.Join(tableRequest.AttributesToGet, ",")
		}
		items := make([]state.DynamoItem, 0, len(tableRequest.Keys))
		for _, key := range tableRequest.Keys {
			item, exists, err := s.store.DynamoGetItem(tableName, key)
			if err != nil {
				writeDynamoStateError(w, err)
				return
			}
			if exists {
				items = append(items, projectDynamoItem(item, projection, tableRequest.ExpressionAttributeNames))
			}
		}
		responses[tableName] = items
	}
	writeDynamoJSON(w, map[string]any{"Responses": responses, "UnprocessedKeys": map[string]any{}})
}

func (s *Server) handleDynamoBatchWrite(w http.ResponseWriter, request dynamoBatchWriteRequest) {
	operations := []state.DynamoWriteOperation{}
	for tableName, writes := range request.RequestItems {
		for _, write := range writes {
			switch {
			case write.PutRequest != nil && write.DeleteRequest == nil:
				operations = append(operations, state.DynamoWriteOperation{Kind: "put", Table: tableName, Item: write.PutRequest.Item})
			case write.DeleteRequest != nil && write.PutRequest == nil:
				operations = append(operations, state.DynamoWriteOperation{Kind: "delete", Table: tableName, Key: write.DeleteRequest.Key})
			default:
				writeDynamoStateError(w, fmt.Errorf("%w: each batch write must contain exactly one PutRequest or DeleteRequest", state.ErrDynamoValidation))
				return
			}
		}
	}
	if len(operations) == 0 || len(operations) > 25 {
		writeDynamoStateError(w, fmt.Errorf("%w: BatchWriteItem must contain 1 to 25 operations", state.ErrDynamoValidation))
		return
	}
	if err := s.store.DynamoTransactWrite(operations); err != nil {
		writeDynamoStateError(w, err)
		return
	}
	writeDynamoJSON(w, map[string]any{"UnprocessedItems": map[string]any{}})
}

func decodeDynamoRequest(w http.ResponseWriter, decoder *json.Decoder, target any) bool {
	if err := decoder.Decode(target); err != nil {
		writeDynamoError(w, "SerializationException", "invalid DynamoDB JSON request")
		return false
	}
	return true
}

func (s *Server) handleDynamoListTables(w http.ResponseWriter, request dynamoListTablesRequest) {
	tables := s.store.ListDynamoTables()
	names := make([]string, 0, len(tables))
	for _, table := range tables {
		if table.Name > request.ExclusiveStartTableName {
			names = append(names, table.Name)
		}
	}
	response := map[string]any{}
	if request.Limit > 0 && len(names) > request.Limit {
		response["LastEvaluatedTableName"] = names[request.Limit-1]
		names = names[:request.Limit]
	}
	response["TableNames"] = names
	writeDynamoJSON(w, response)
}

func (s *Server) handleDynamoItemOperation(w http.ResponseWriter, operation string, request dynamoItemRequest) {
	table, err := s.store.DynamoTable(request.TableName)
	if err != nil {
		writeDynamoStateError(w, err)
		return
	}
	condition, err := compileDynamoCondition(request.ConditionExpression, request.ExpressionAttributeNames, request.ExpressionAttributeValues)
	if err != nil {
		writeDynamoStateError(w, err)
		return
	}
	switch operation {
	case "PutItem":
		old, existed, err := s.store.DynamoPutItem(request.TableName, request.Item, condition)
		if err != nil {
			writeDynamoStateError(w, err)
			return
		}
		response := map[string]any{}
		if strings.EqualFold(request.ReturnValues, "ALL_OLD") && existed {
			response["Attributes"] = old
		}
		writeDynamoJSON(w, response)
	case "GetItem":
		item, exists, err := s.store.DynamoGetItem(request.TableName, request.Key)
		if err != nil {
			writeDynamoStateError(w, err)
			return
		}
		response := map[string]any{}
		if exists {
			response["Item"] = projectDynamoItem(item, request.ProjectionExpression, request.ExpressionAttributeNames)
		}
		writeDynamoJSON(w, response)
	case "DeleteItem":
		old, existed, err := s.store.DynamoDeleteItem(request.TableName, request.Key, condition)
		if err != nil {
			writeDynamoStateError(w, err)
			return
		}
		response := map[string]any{}
		if strings.EqualFold(request.ReturnValues, "ALL_OLD") && existed {
			response["Attributes"] = old
		}
		writeDynamoJSON(w, response)
	case "UpdateItem":
		updatedNames := []string{}
		old, next, err := s.store.DynamoUpdateItem(request.TableName, request.Key, condition, func(item state.DynamoItem) (state.DynamoItem, error) {
			var applyErr error
			item, updatedNames, applyErr = applyDynamoUpdate(item, request.UpdateExpression, request.ExpressionAttributeNames, request.ExpressionAttributeValues)
			return item, applyErr
		})
		if err != nil {
			writeDynamoStateError(w, err)
			return
		}
		response := map[string]any{}
		if attributes := dynamoReturnAttributes(request.ReturnValues, old, next, updatedNames); attributes != nil {
			response["Attributes"] = attributes
		}
		writeDynamoJSON(w, response)
	case "Query":
		s.handleDynamoQuery(w, table, request)
	case "Scan":
		s.handleDynamoScan(w, table, request)
	}
}

func (s *Server) handleDynamoQuery(w http.ResponseWriter, table state.DynamoTable, request dynamoItemRequest) {
	keyCondition, err := compileDynamoCondition(request.KeyConditionExpression, request.ExpressionAttributeNames, request.ExpressionAttributeValues)
	if err != nil || keyCondition == nil {
		if err == nil {
			err = fmt.Errorf("%w: KeyConditionExpression is required", state.ErrDynamoValidation)
		}
		writeDynamoStateError(w, err)
		return
	}
	filter, err := compileDynamoCondition(request.FilterExpression, request.ExpressionAttributeNames, request.ExpressionAttributeValues)
	if err != nil {
		writeDynamoStateError(w, err)
		return
	}
	items, err := s.store.DynamoListItems(table.Name)
	if err != nil {
		writeDynamoStateError(w, err)
		return
	}
	if request.ScanIndexForward != nil && !*request.ScanIndexForward {
		reverseDynamoItems(items)
	}
	responseItems, scanned, lastKey := selectDynamoItems(table, items, request, keyCondition, filter)
	response := map[string]any{"Count": len(responseItems), "ScannedCount": scanned}
	if !strings.EqualFold(request.Select, "COUNT") {
		response["Items"] = responseItems
	}
	if lastKey != nil {
		response["LastEvaluatedKey"] = lastKey
	}
	writeDynamoJSON(w, response)
}

func (s *Server) handleDynamoScan(w http.ResponseWriter, table state.DynamoTable, request dynamoItemRequest) {
	filter, err := compileDynamoCondition(request.FilterExpression, request.ExpressionAttributeNames, request.ExpressionAttributeValues)
	if err != nil {
		writeDynamoStateError(w, err)
		return
	}
	items, err := s.store.DynamoListItems(table.Name)
	if err != nil {
		writeDynamoStateError(w, err)
		return
	}
	responseItems, scanned, lastKey := selectDynamoItems(table, items, request, nil, filter)
	response := map[string]any{"Count": len(responseItems), "ScannedCount": scanned}
	if !strings.EqualFold(request.Select, "COUNT") {
		response["Items"] = responseItems
	}
	if lastKey != nil {
		response["LastEvaluatedKey"] = lastKey
	}
	writeDynamoJSON(w, response)
}

func (s *Server) handleDynamoTransactWrite(w http.ResponseWriter, request dynamoTransactWriteRequest) {
	if len(request.TransactItems) == 0 || len(request.TransactItems) > 100 {
		writeDynamoStateError(w, fmt.Errorf("%w: TransactItems must contain 1 to 100 items", state.ErrDynamoValidation))
		return
	}
	operations := make([]state.DynamoWriteOperation, 0, len(request.TransactItems))
	for _, item := range request.TransactItems {
		switch {
		case item.Put != nil && item.Delete == nil:
			operations = append(operations, state.DynamoWriteOperation{Kind: "put", Table: item.Put.TableName, Item: item.Put.Item})
		case item.Delete != nil && item.Put == nil:
			operations = append(operations, state.DynamoWriteOperation{Kind: "delete", Table: item.Delete.TableName, Key: item.Delete.Key})
		default:
			writeDynamoStateError(w, fmt.Errorf("%w: unsupported transaction item", state.ErrDynamoValidation))
			return
		}
	}
	if err := s.store.DynamoTransactWrite(operations); err != nil {
		writeDynamoStateError(w, err)
		return
	}
	writeDynamoJSON(w, map[string]any{})
}

func selectDynamoItems(table state.DynamoTable, items []state.DynamoItem, request dynamoItemRequest, keyCondition, filter state.DynamoCondition) ([]state.DynamoItem, int, state.DynamoItem) {
	result := []state.DynamoItem{}
	started := len(request.ExclusiveStartKey) == 0
	scanned := 0
	var lastKey state.DynamoItem
	for index, item := range items {
		if !started {
			started = dynamoKeysEqual(table, item, request.ExclusiveStartKey)
			continue
		}
		if keyCondition != nil && !keyCondition(item, true) {
			continue
		}
		scanned++
		if filter == nil || filter(item, true) {
			result = append(result, projectDynamoItem(item, request.ProjectionExpression, request.ExpressionAttributeNames))
		}
		if request.Limit > 0 && scanned >= request.Limit {
			if index < len(items)-1 {
				lastKey = dynamoPrimaryKey(table, item)
			}
			break
		}
	}
	return result, scanned, lastKey
}

func dynamoTableDescription(table state.DynamoTable) map[string]any {
	return map[string]any{
		"AttributeDefinitions":  table.AttributeDefinitions,
		"BillingModeSummary":    map[string]any{"BillingMode": table.BillingMode},
		"CreationDateTime":      table.CreatedAt.Unix(),
		"ItemCount":             len(table.Items),
		"KeySchema":             table.KeySchema,
		"ProvisionedThroughput": map[string]any{"NumberOfDecreasesToday": 0, "ReadCapacityUnits": 0, "WriteCapacityUnits": 0},
		"TableArn":              "arn:aws:dynamodb:us-east-1:000000000000:table/" + table.Name,
		"TableId":               "fcp-" + table.Name,
		"TableName":             table.Name,
		"TableSizeBytes":        0,
		"TableStatus":           table.Status,
	}
}

func dynamoPrimaryKey(table state.DynamoTable, item state.DynamoItem) state.DynamoItem {
	key := state.DynamoItem{}
	for _, element := range table.KeySchema {
		if value, ok := item[element.AttributeName]; ok {
			key[element.AttributeName] = value
		}
	}
	return key
}

func dynamoKeysEqual(table state.DynamoTable, left, right state.DynamoItem) bool {
	for _, element := range table.KeySchema {
		if !dynamoAttributeEqual(left[element.AttributeName], right[element.AttributeName]) {
			return false
		}
	}
	return true
}

func reverseDynamoItems(items []state.DynamoItem) {
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
}

func projectDynamoItem(item state.DynamoItem, expression string, names map[string]string) state.DynamoItem {
	if strings.TrimSpace(expression) == "" {
		return item
	}
	result := state.DynamoItem{}
	for _, raw := range strings.Split(expression, ",") {
		name := resolveDynamoName(strings.TrimSpace(raw), names)
		if value, ok := item[name]; ok {
			result[name] = value
		}
	}
	return result
}

func dynamoReturnAttributes(mode string, old, next state.DynamoItem, updated []string) state.DynamoItem {
	switch strings.ToUpper(mode) {
	case "ALL_OLD":
		return old
	case "ALL_NEW":
		return next
	case "UPDATED_OLD":
		return selectDynamoAttributes(old, updated)
	case "UPDATED_NEW":
		return selectDynamoAttributes(next, updated)
	default:
		return nil
	}
}

func selectDynamoAttributes(item state.DynamoItem, names []string) state.DynamoItem {
	result := state.DynamoItem{}
	for _, name := range names {
		if value, ok := item[name]; ok {
			result[name] = value
		}
	}
	return result
}

func compileDynamoCondition(expression string, names map[string]string, values map[string]state.DynamoAttributeValue) (state.DynamoCondition, error) {
	if strings.TrimSpace(expression) == "" {
		return nil, nil
	}
	parser := dynamoExpressionParser{tokens: tokenizeDynamoExpression(expression), names: names, values: values}
	evaluator, err := parser.parseExpression()
	if err != nil || parser.position != len(parser.tokens) {
		if err == nil {
			err = fmt.Errorf("unexpected token %q", parser.tokens[parser.position])
		}
		return nil, fmt.Errorf("%w: invalid expression: %v", state.ErrDynamoValidation, err)
	}
	return func(item state.DynamoItem, exists bool) bool { return evaluator(item, exists) }, nil
}

type dynamoEvaluator func(state.DynamoItem, bool) bool

type dynamoExpressionParser struct {
	tokens   []string
	position int
	names    map[string]string
	values   map[string]state.DynamoAttributeValue
}

func (p *dynamoExpressionParser) parseExpression() (dynamoEvaluator, error) {
	left, err := p.parsePredicate()
	if err != nil {
		return nil, err
	}
	for p.match("AND") {
		right, err := p.parsePredicate()
		if err != nil {
			return nil, err
		}
		previous := left
		left = func(item state.DynamoItem, exists bool) bool { return previous(item, exists) && right(item, exists) }
	}
	return left, nil
}

func (p *dynamoExpressionParser) parsePredicate() (dynamoEvaluator, error) {
	if p.match("(") {
		nested, err := p.parseExpression()
		if err != nil || !p.match(")") {
			return nil, fmt.Errorf("unclosed expression")
		}
		return nested, nil
	}
	if p.peekEqual("attribute_exists") || p.peekEqual("attribute_not_exists") {
		function := strings.ToLower(p.next())
		if !p.match("(") {
			return nil, fmt.Errorf("expected (")
		}
		name := resolveDynamoName(p.next(), p.names)
		if name == "" || !p.match(")") {
			return nil, fmt.Errorf("invalid attribute function")
		}
		return func(item state.DynamoItem, exists bool) bool {
			_, present := item[name]
			if function == "attribute_exists" {
				return exists && present
			}
			return !exists || !present
		}, nil
	}
	if p.peekEqual("begins_with") {
		p.next()
		if !p.match("(") {
			return nil, fmt.Errorf("expected (")
		}
		name := resolveDynamoName(p.next(), p.names)
		if !p.match(",") {
			return nil, fmt.Errorf("expected comma")
		}
		expected, ok := p.values[p.next()]
		if !ok || !p.match(")") {
			return nil, fmt.Errorf("invalid begins_with value")
		}
		return func(item state.DynamoItem, _ bool) bool {
			actual, present := item[name]
			return present && dynamoBeginsWith(actual, expected)
		}, nil
	}
	name := resolveDynamoName(p.next(), p.names)
	if name == "" {
		return nil, fmt.Errorf("attribute name is required")
	}
	if p.match("BETWEEN") {
		lower, lowerOK := p.values[p.next()]
		if !lowerOK || !p.match("AND") {
			return nil, fmt.Errorf("invalid BETWEEN expression")
		}
		upper, upperOK := p.values[p.next()]
		if !upperOK {
			return nil, fmt.Errorf("invalid BETWEEN upper value")
		}
		return func(item state.DynamoItem, _ bool) bool {
			actual, present := item[name]
			return present && dynamoCompare(actual, lower) >= 0 && dynamoCompare(actual, upper) <= 0
		}, nil
	}
	operator := p.next()
	expected, ok := p.values[p.next()]
	if !ok || !map[string]bool{"=": true, "<>": true, "<": true, "<=": true, ">": true, ">=": true}[operator] {
		return nil, fmt.Errorf("invalid comparison")
	}
	return func(item state.DynamoItem, _ bool) bool {
		actual, present := item[name]
		if !present {
			return false
		}
		comparison := dynamoCompare(actual, expected)
		switch operator {
		case "=":
			return comparison == 0
		case "<>":
			return comparison != 0
		case "<":
			return comparison < 0
		case "<=":
			return comparison <= 0
		case ">":
			return comparison > 0
		default:
			return comparison >= 0
		}
	}, nil
}

func (p *dynamoExpressionParser) next() string {
	if p.position >= len(p.tokens) {
		return ""
	}
	token := p.tokens[p.position]
	p.position++
	return token
}

func (p *dynamoExpressionParser) match(value string) bool {
	if !p.peekEqual(value) {
		return false
	}
	p.position++
	return true
}

func (p *dynamoExpressionParser) peekEqual(value string) bool {
	return p.position < len(p.tokens) && strings.EqualFold(p.tokens[p.position], value)
}

func tokenizeDynamoExpression(expression string) []string {
	tokens := []string{}
	for index := 0; index < len(expression); {
		r := rune(expression[index])
		if unicode.IsSpace(r) {
			index++
			continue
		}
		if strings.ContainsRune("(),=", r) {
			tokens = append(tokens, string(r))
			index++
			continue
		}
		if r == '<' || r == '>' {
			end := index + 1
			if end < len(expression) && (expression[end] == '=' || (r == '<' && expression[end] == '>')) {
				end++
			}
			tokens = append(tokens, expression[index:end])
			index = end
			continue
		}
		end := index
		for end < len(expression) && !unicode.IsSpace(rune(expression[end])) && !strings.ContainsRune("(),=<> ", rune(expression[end])) {
			end++
		}
		tokens = append(tokens, expression[index:end])
		index = end
	}
	return tokens
}

func applyDynamoUpdate(item state.DynamoItem, expression string, names map[string]string, values map[string]state.DynamoAttributeValue) (state.DynamoItem, []string, error) {
	sections := splitDynamoUpdateSections(expression)
	if len(sections) == 0 {
		return nil, nil, fmt.Errorf("%w: UpdateExpression is required", state.ErrDynamoValidation)
	}
	updated := []string{}
	for _, section := range sections {
		switch section.kind {
		case "SET":
			for _, assignment := range splitDynamoTopLevel(section.body, ',') {
				parts := strings.SplitN(assignment, "=", 2)
				if len(parts) != 2 {
					return nil, nil, fmt.Errorf("%w: invalid SET expression", state.ErrDynamoValidation)
				}
				name := resolveDynamoName(strings.TrimSpace(parts[0]), names)
				right := strings.TrimSpace(parts[1])
				if strings.HasPrefix(strings.ToLower(right), "if_not_exists(") && strings.HasSuffix(right, ")") {
					arguments := splitDynamoTopLevel(right[len("if_not_exists("):len(right)-1], ',')
					if len(arguments) != 2 {
						return nil, nil, fmt.Errorf("%w: invalid if_not_exists", state.ErrDynamoValidation)
					}
					if _, exists := item[resolveDynamoName(strings.TrimSpace(arguments[0]), names)]; exists {
						continue
					}
					right = strings.TrimSpace(arguments[1])
				}
				value, ok := values[right]
				if !ok {
					return nil, nil, fmt.Errorf("%w: missing expression value", state.ErrDynamoValidation)
				}
				item[name] = value
				updated = append(updated, name)
			}
		case "REMOVE":
			for _, raw := range strings.Split(section.body, ",") {
				for _, token := range strings.Fields(raw) {
					name := resolveDynamoName(token, names)
					delete(item, name)
					updated = append(updated, name)
				}
			}
		case "ADD":
			for _, addition := range splitDynamoTopLevel(section.body, ',') {
				parts := strings.Fields(addition)
				if len(parts) != 2 {
					return nil, nil, fmt.Errorf("%w: invalid ADD expression", state.ErrDynamoValidation)
				}
				name := resolveDynamoName(parts[0], names)
				increment, ok := values[parts[1]]
				if !ok || increment.N == nil {
					return nil, nil, fmt.Errorf("%w: ADD currently requires a number", state.ErrDynamoValidation)
				}
				current := "0"
				if existing, present := item[name]; present {
					if existing.N == nil {
						return nil, nil, fmt.Errorf("%w: ADD target is not a number", state.ErrDynamoValidation)
					}
					current = *existing.N
				}
				sum, err := addDynamoNumbers(current, *increment.N)
				if err != nil {
					return nil, nil, err
				}
				item[name] = state.DynamoAttributeValue{N: &sum}
				updated = append(updated, name)
			}
		default:
			return nil, nil, fmt.Errorf("%w: unsupported update section %s", state.ErrDynamoValidation, section.kind)
		}
	}
	return item, updated, nil
}

type dynamoUpdateSection struct {
	kind string
	body string
}

var dynamoUpdateKeyword = regexp.MustCompile(`(?i)(^|\s)(SET|REMOVE|ADD|DELETE)\s+`)

func splitDynamoUpdateSections(expression string) []dynamoUpdateSection {
	matches := dynamoUpdateKeyword.FindAllStringSubmatchIndex(expression, -1)
	sections := make([]dynamoUpdateSection, 0, len(matches))
	for index, match := range matches {
		bodyStart := match[1]
		bodyEnd := len(expression)
		if index+1 < len(matches) {
			bodyEnd = matches[index+1][0]
		}
		sections = append(sections, dynamoUpdateSection{kind: strings.ToUpper(expression[match[4]:match[5]]), body: strings.TrimSpace(expression[bodyStart:bodyEnd])})
	}
	return sections
}

func splitDynamoTopLevel(value string, separator rune) []string {
	parts := []string{}
	depth, start := 0, 0
	for index, current := range value {
		switch current {
		case '(':
			depth++
		case ')':
			depth--
		default:
			if current == separator && depth == 0 {
				parts = append(parts, strings.TrimSpace(value[start:index]))
				start = index + 1
			}
		}
	}
	parts = append(parts, strings.TrimSpace(value[start:]))
	return parts
}

func resolveDynamoName(value string, names map[string]string) string {
	value = strings.TrimSpace(value)
	if mapped, ok := names[value]; ok {
		return mapped
	}
	return value
}

func dynamoAttributeEqual(left, right state.DynamoAttributeValue) bool {
	leftRaw, _ := json.Marshal(left)
	rightRaw, _ := json.Marshal(right)
	return string(leftRaw) == string(rightRaw)
}

func dynamoCompare(left, right state.DynamoAttributeValue) int {
	switch {
	case left.S != nil && right.S != nil:
		return strings.Compare(*left.S, *right.S)
	case left.N != nil && right.N != nil:
		leftNumber, leftOK := new(big.Rat).SetString(*left.N)
		rightNumber, rightOK := new(big.Rat).SetString(*right.N)
		if leftOK && rightOK {
			return leftNumber.Cmp(rightNumber)
		}
	case left.B != nil && right.B != nil:
		return strings.Compare(*left.B, *right.B)
	}
	if dynamoAttributeEqual(left, right) {
		return 0
	}
	return 1
}

func dynamoBeginsWith(actual, prefix state.DynamoAttributeValue) bool {
	if actual.S != nil && prefix.S != nil {
		return strings.HasPrefix(*actual.S, *prefix.S)
	}
	if actual.B != nil && prefix.B != nil {
		return strings.HasPrefix(*actual.B, *prefix.B)
	}
	return false
}

func addDynamoNumbers(left, right string) (string, error) {
	leftNumber, leftOK := new(big.Rat).SetString(left)
	rightNumber, rightOK := new(big.Rat).SetString(right)
	if !leftOK || !rightOK {
		return "", fmt.Errorf("%w: invalid DynamoDB number", state.ErrDynamoValidation)
	}
	result := new(big.Rat).Add(leftNumber, rightNumber)
	if result.IsInt() {
		return result.Num().String(), nil
	}
	text := result.FloatString(38)
	text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	return text, nil
}

func writeDynamoJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	w.Header().Set("x-amzn-RequestId", requestID())
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(value)
}

func writeDynamoStateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, state.ErrDynamoTableNotFound):
		writeDynamoError(w, "ResourceNotFoundException", "Requested resource not found")
	case errors.Is(err, state.ErrDynamoTableExists):
		writeDynamoError(w, "ResourceInUseException", "Table already exists")
	case errors.Is(err, state.ErrDynamoConditionalCheckFailed):
		writeDynamoError(w, "ConditionalCheckFailedException", "The conditional request failed")
	case errors.Is(err, state.ErrDynamoValidation):
		writeDynamoError(w, "ValidationException", strings.TrimPrefix(err.Error(), state.ErrDynamoValidation.Error()+": "))
	default:
		writeDynamoError(w, "InternalServerError", err.Error())
	}
}

func writeDynamoError(w http.ResponseWriter, code, message string) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	w.Header().Set("x-amzn-RequestId", requestID())
	w.Header().Set("x-amzn-ErrorType", code)
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"__type": "com.amazonaws.dynamodb.v20120810#" + code, "message": message})
}
