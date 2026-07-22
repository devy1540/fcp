package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var dynamoTableNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,255}$`)

var (
	ErrDynamoTableNotFound          = errors.New("DynamoDB table not found")
	ErrDynamoTableExists            = errors.New("DynamoDB table already exists")
	ErrDynamoConditionalCheckFailed = errors.New("DynamoDB conditional check failed")
	ErrDynamoValidation             = errors.New("DynamoDB validation failed")
)

// DynamoAttributeValue mirrors DynamoDB's JSON wire representation. Pointer
// fields preserve meaningful false and NULL values while keeping snapshots
// compact and directly serializable in API responses.
type DynamoAttributeValue struct {
	B    *string                         `json:"B,omitempty"`
	BOOL *bool                           `json:"BOOL,omitempty"`
	BS   []string                        `json:"BS,omitempty"`
	L    []DynamoAttributeValue          `json:"L,omitempty"`
	M    map[string]DynamoAttributeValue `json:"M,omitempty"`
	N    *string                         `json:"N,omitempty"`
	NS   []string                        `json:"NS,omitempty"`
	NULL *bool                           `json:"NULL,omitempty"`
	S    *string                         `json:"S,omitempty"`
	SS   []string                        `json:"SS,omitempty"`
}

func (value DynamoAttributeValue) MarshalJSON() ([]byte, error) {
	switch {
	case value.B != nil:
		return json.Marshal(map[string]any{"B": *value.B})
	case value.BOOL != nil:
		return json.Marshal(map[string]any{"BOOL": *value.BOOL})
	case value.BS != nil:
		return json.Marshal(map[string]any{"BS": value.BS})
	case value.L != nil:
		return json.Marshal(map[string]any{"L": value.L})
	case value.M != nil:
		return json.Marshal(map[string]any{"M": value.M})
	case value.N != nil:
		return json.Marshal(map[string]any{"N": *value.N})
	case value.NS != nil:
		return json.Marshal(map[string]any{"NS": value.NS})
	case value.NULL != nil:
		return json.Marshal(map[string]any{"NULL": *value.NULL})
	case value.S != nil:
		return json.Marshal(map[string]any{"S": *value.S})
	case value.SS != nil:
		return json.Marshal(map[string]any{"SS": value.SS})
	default:
		return nil, fmt.Errorf("%w: empty attribute value", ErrDynamoValidation)
	}
}

type DynamoItem map[string]DynamoAttributeValue

type DynamoKeySchemaElement struct {
	AttributeName string `json:"AttributeName"`
	KeyType       string `json:"KeyType"`
}

type DynamoAttributeDefinition struct {
	AttributeName string `json:"AttributeName"`
	AttributeType string `json:"AttributeType"`
}

type DynamoTable struct {
	Name                 string                      `json:"name"`
	CreatedAt            time.Time                   `json:"createdAt"`
	Status               string                      `json:"status"`
	BillingMode          string                      `json:"billingMode"`
	KeySchema            []DynamoKeySchemaElement    `json:"keySchema"`
	AttributeDefinitions []DynamoAttributeDefinition `json:"attributeDefinitions"`
	Items                map[string]DynamoItem       `json:"items"`
}

type DynamoWriteOperation struct {
	Kind  string
	Table string
	Item  DynamoItem
	Key   DynamoItem
}

type DynamoCondition func(item DynamoItem, exists bool) bool
type DynamoUpdate func(item DynamoItem) (DynamoItem, error)

func (s *Store) CreateDynamoTable(name string, keySchema []DynamoKeySchemaElement, definitions []DynamoAttributeDefinition, billingMode string) (DynamoTable, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data.DynamoTables[name]; exists {
		return DynamoTable{}, ErrDynamoTableExists
	}
	if err := validateDynamoTable(name, keySchema, definitions); err != nil {
		return DynamoTable{}, err
	}
	if billingMode == "" {
		billingMode = "PROVISIONED"
	}
	table := &DynamoTable{
		Name: name, CreatedAt: s.now().UTC(), Status: "ACTIVE", BillingMode: billingMode,
		KeySchema:            append([]DynamoKeySchemaElement(nil), keySchema...),
		AttributeDefinitions: append([]DynamoAttributeDefinition(nil), definitions...),
		Items:                map[string]DynamoItem{},
	}
	s.data.DynamoTables[name] = table
	if err := s.saveLocked(); err != nil {
		delete(s.data.DynamoTables, name)
		return DynamoTable{}, err
	}
	return cloneDynamoTable(table), nil
}

func (s *Store) DynamoTable(name string) (DynamoTable, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	table, ok := s.data.DynamoTables[name]
	if !ok {
		return DynamoTable{}, ErrDynamoTableNotFound
	}
	return cloneDynamoTable(table), nil
}

func (s *Store) ListDynamoTables() []DynamoTable {
	s.mu.Lock()
	defer s.mu.Unlock()
	tables := make([]DynamoTable, 0, len(s.data.DynamoTables))
	for _, table := range s.data.DynamoTables {
		tables = append(tables, cloneDynamoTable(table))
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })
	return tables
}

func (s *Store) DeleteDynamoTable(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.DynamoTables[name]; !ok {
		return ErrDynamoTableNotFound
	}
	delete(s.data.DynamoTables, name)
	return s.saveLocked()
}

func (s *Store) ClearDynamoTable(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	table, ok := s.data.DynamoTables[name]
	if !ok {
		return ErrDynamoTableNotFound
	}
	table.Items = map[string]DynamoItem{}
	return s.saveLocked()
}

func (s *Store) DynamoGetItem(tableName string, key DynamoItem) (DynamoItem, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	table, ok := s.data.DynamoTables[tableName]
	if !ok {
		return nil, false, ErrDynamoTableNotFound
	}
	encoded, err := dynamoItemKey(table, key)
	if err != nil {
		return nil, false, err
	}
	item, exists := table.Items[encoded]
	return cloneDynamoItem(item), exists, nil
}

func (s *Store) DynamoListItems(tableName string) ([]DynamoItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	table, ok := s.data.DynamoTables[tableName]
	if !ok {
		return nil, ErrDynamoTableNotFound
	}
	keys := make([]string, 0, len(table.Items))
	for key := range table.Items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]DynamoItem, 0, len(keys))
	for _, key := range keys {
		items = append(items, cloneDynamoItem(table.Items[key]))
	}
	return items, nil
}

func (s *Store) DynamoPutItem(tableName string, item DynamoItem, condition DynamoCondition) (DynamoItem, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	table, ok := s.data.DynamoTables[tableName]
	if !ok {
		return nil, false, ErrDynamoTableNotFound
	}
	key, err := dynamoItemKey(table, item)
	if err != nil {
		return nil, false, err
	}
	old, existed := table.Items[key]
	if condition != nil && !condition(cloneDynamoItem(old), existed) {
		return nil, false, ErrDynamoConditionalCheckFailed
	}
	table.Items[key] = cloneDynamoItem(item)
	if err := s.saveLocked(); err != nil {
		if existed {
			table.Items[key] = old
		} else {
			delete(table.Items, key)
		}
		return nil, false, err
	}
	return cloneDynamoItem(old), existed, nil
}

func (s *Store) DynamoDeleteItem(tableName string, keyItem DynamoItem, condition DynamoCondition) (DynamoItem, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	table, ok := s.data.DynamoTables[tableName]
	if !ok {
		return nil, false, ErrDynamoTableNotFound
	}
	key, err := dynamoItemKey(table, keyItem)
	if err != nil {
		return nil, false, err
	}
	old, existed := table.Items[key]
	if condition != nil && !condition(cloneDynamoItem(old), existed) {
		return nil, false, ErrDynamoConditionalCheckFailed
	}
	if !existed {
		return nil, false, nil
	}
	delete(table.Items, key)
	if err := s.saveLocked(); err != nil {
		table.Items[key] = old
		return nil, false, err
	}
	return cloneDynamoItem(old), true, nil
}

func (s *Store) DynamoUpdateItem(tableName string, keyItem DynamoItem, condition DynamoCondition, update DynamoUpdate) (DynamoItem, DynamoItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	table, ok := s.data.DynamoTables[tableName]
	if !ok {
		return nil, nil, ErrDynamoTableNotFound
	}
	key, err := dynamoItemKey(table, keyItem)
	if err != nil {
		return nil, nil, err
	}
	old, existed := table.Items[key]
	if condition != nil && !condition(cloneDynamoItem(old), existed) {
		return nil, nil, ErrDynamoConditionalCheckFailed
	}
	base := cloneDynamoItem(old)
	if !existed {
		base = cloneDynamoItem(keyItem)
	}
	next, err := update(base)
	if err != nil {
		return nil, nil, err
	}
	nextKey, err := dynamoItemKey(table, next)
	if err != nil {
		return nil, nil, err
	}
	if nextKey != key {
		return nil, nil, fmt.Errorf("%w: primary key attributes cannot be updated", ErrDynamoValidation)
	}
	table.Items[key] = cloneDynamoItem(next)
	if err := s.saveLocked(); err != nil {
		if existed {
			table.Items[key] = old
		} else {
			delete(table.Items, key)
		}
		return nil, nil, err
	}
	return cloneDynamoItem(old), cloneDynamoItem(next), nil
}

func (s *Store) DynamoTransactWrite(operations []DynamoWriteOperation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	type resolved struct {
		table *DynamoTable
		key   string
		op    DynamoWriteOperation
	}
	resolvedOps := make([]resolved, 0, len(operations))
	seen := map[string]bool{}
	for _, operation := range operations {
		table, ok := s.data.DynamoTables[operation.Table]
		if !ok {
			return ErrDynamoTableNotFound
		}
		candidate := operation.Key
		if strings.EqualFold(operation.Kind, "put") {
			candidate = operation.Item
		}
		key, err := dynamoItemKey(table, candidate)
		if err != nil {
			return err
		}
		identity := table.Name + "\x00" + key
		if seen[identity] {
			return fmt.Errorf("%w: transaction contains multiple operations for one item", ErrDynamoValidation)
		}
		seen[identity] = true
		resolvedOps = append(resolvedOps, resolved{table: table, key: key, op: operation})
	}
	backup := make([]struct {
		item   DynamoItem
		exists bool
	}, len(resolvedOps))
	for index, operation := range resolvedOps {
		backup[index].item, backup[index].exists = operation.table.Items[operation.key]
		switch strings.ToLower(operation.op.Kind) {
		case "put":
			operation.table.Items[operation.key] = cloneDynamoItem(operation.op.Item)
		case "delete":
			delete(operation.table.Items, operation.key)
		default:
			return fmt.Errorf("%w: unsupported transaction operation", ErrDynamoValidation)
		}
	}
	if err := s.saveLocked(); err != nil {
		for index, operation := range resolvedOps {
			if backup[index].exists {
				operation.table.Items[operation.key] = backup[index].item
			} else {
				delete(operation.table.Items, operation.key)
			}
		}
		return err
	}
	return nil
}

func validateDynamoTable(name string, keySchema []DynamoKeySchemaElement, definitions []DynamoAttributeDefinition) error {
	if !dynamoTableNamePattern.MatchString(name) {
		return fmt.Errorf("%w: invalid table name", ErrDynamoValidation)
	}
	definitionNames := map[string]bool{}
	for _, definition := range definitions {
		if definition.AttributeName == "" || (definition.AttributeType != "S" && definition.AttributeType != "N" && definition.AttributeType != "B") {
			return fmt.Errorf("%w: invalid attribute definition", ErrDynamoValidation)
		}
		definitionNames[definition.AttributeName] = true
	}
	hashCount, rangeCount := 0, 0
	for _, element := range keySchema {
		if !definitionNames[element.AttributeName] {
			return fmt.Errorf("%w: key attribute is not defined", ErrDynamoValidation)
		}
		switch element.KeyType {
		case "HASH":
			hashCount++
		case "RANGE":
			rangeCount++
		default:
			return fmt.Errorf("%w: invalid key type", ErrDynamoValidation)
		}
	}
	if hashCount != 1 || rangeCount > 1 || len(keySchema) != hashCount+rangeCount {
		return fmt.Errorf("%w: invalid key schema", ErrDynamoValidation)
	}
	if len(definitions) != len(keySchema) {
		return fmt.Errorf("%w: attribute definitions must match the key schema", ErrDynamoValidation)
	}
	return nil
}

func dynamoItemKey(table *DynamoTable, item DynamoItem) (string, error) {
	parts := make([]string, 0, len(table.KeySchema))
	for _, element := range table.KeySchema {
		value, ok := item[element.AttributeName]
		if !ok {
			return "", fmt.Errorf("%w: missing key %s", ErrDynamoValidation, element.AttributeName)
		}
		encoded, err := dynamoScalarKey(value)
		if err != nil {
			return "", err
		}
		parts = append(parts, element.KeyType+"\x00"+element.AttributeName+"\x00"+encoded)
	}
	return strings.Join(parts, "\x01"), nil
}

func dynamoScalarKey(value DynamoAttributeValue) (string, error) {
	switch {
	case value.S != nil:
		return "S\x00" + *value.S, nil
	case value.N != nil:
		return "N\x00" + *value.N, nil
	case value.B != nil:
		return "B\x00" + *value.B, nil
	default:
		return "", fmt.Errorf("%w: key must be a scalar S, N or B value", ErrDynamoValidation)
	}
}

func cloneDynamoTable(table *DynamoTable) DynamoTable {
	cloned := *table
	cloned.KeySchema = append([]DynamoKeySchemaElement(nil), table.KeySchema...)
	cloned.AttributeDefinitions = append([]DynamoAttributeDefinition(nil), table.AttributeDefinitions...)
	cloned.Items = make(map[string]DynamoItem, len(table.Items))
	for key, item := range table.Items {
		cloned.Items[key] = cloneDynamoItem(item)
	}
	return cloned
}

func cloneDynamoItem(item DynamoItem) DynamoItem {
	if item == nil {
		return nil
	}
	cloned := make(DynamoItem, len(item))
	for key, value := range item {
		cloned[key] = cloneDynamoAttributeValue(value)
	}
	return cloned
}

func cloneDynamoAttributeValue(value DynamoAttributeValue) DynamoAttributeValue {
	// JSON round-tripping keeps the clone implementation aligned with the wire
	// model when new AttributeValue fields are added.
	raw, _ := json.Marshal(value)
	var cloned DynamoAttributeValue
	_ = json.Unmarshal(raw, &cloned)
	return cloned
}
