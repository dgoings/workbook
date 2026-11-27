package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// How a compiled schema judges a relic. Evaluation follows JSON Schema:
// lengths count Unicode code points, an integer is any number with no
// fraction, numbers compare exactly by value, enum and const compare values
// as JSON Schema does (numbers by value, at any depth), and required checks
// only that a name is present.

// RelicSchemaProblem is the first thing about a relic, or a piece of one, that
// does not fit a schema: where it is, the word it breaks and what is wrong.
type RelicSchemaProblem struct {
	Where   string
	Keyword string
	Message string
}

// String is "<where>: <message> (<keyword>)".
func (p RelicSchemaProblem) String() string {
	if p.Keyword == "" {
		return p.Where + ": " + p.Message
	}
	return p.Where + ": " + p.Message + " (" + p.Keyword + ")"
}

// schemaFault is a problem inside one value: pointer is the path from the
// value to the place that breaks the rule.
type schemaFault struct {
	pointer []string
	keyword string
	message string
}

func (n *schemaNode) chain() []*schemaNode {
	var chain []*schemaNode
	for node := n; node != nil; node = node.ref {
		chain = append(chain, node)
	}
	return chain
}

func decodeSchemaInstance(raw []byte) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	return value, true
}

func appendPointer(pointer []string, token string) []string {
	return append(append(make([]string, 0, len(pointer)+1), pointer...), token)
}

// schemaCheck is one check of one value. It remembers what it has judged, so
// each schema node is judged against each object or list in the value once
// however many $ref paths lead to it, and it counts its work: past budget
// steps every judgement is a problem saying the schema is too costly to
// check, never a pass.
type schemaCheck struct {
	memo    map[schemaCheckKey]*schemaFault
	steps   int64
	budget  int64
	started bool
	root    []string // where the check began, which a too costly problem names
}

// schemaCheckKey is one schema node and one object or list in the value, by
// identity: a decoded value holds each container once, at one place, so its
// identity stands for its place.
type schemaCheckKey struct {
	node      *schemaNode
	container uintptr
}

// maxSchemaCheckMemo bounds what one check remembers, so a schema built to
// reach the step budget cannot also take gigabytes. Past it a judgement is
// made again when it comes up again, which costs steps and changes no result.
const maxSchemaCheckMemo = 1 << 20

func newSchemaCheck(budget int64) *schemaCheck {
	return &schemaCheck{memo: map[schemaCheckKey]*schemaFault{}, budget: budget}
}

// step counts one piece of work and reports whether the budget is spent.
func (c *schemaCheck) step() bool {
	c.steps++
	return c.steps > c.budget
}

func (c *schemaCheck) tooCostly() *schemaFault {
	return &schemaFault{c.root, "", "the schema is too costly to check against this value"}
}

// checkValue judges one value against a node and every node its $ref chain
// reaches. noun names the members of the value when it is an object:
// "property", "field" or "member".
func checkValue(node *schemaNode, value any, noun string, at []string) *schemaFault {
	return newSchemaCheck(MaxRelicSchemaCheckSteps).value(node, value, noun, at)
}

// value is checkValue within one check. A non-empty object or list is judged
// against a node once; scalars and empty containers cost no more than their
// node's chain, so they are not remembered.
func (c *schemaCheck) value(node *schemaNode, value any, noun string, at []string) *schemaFault {
	if !c.started {
		c.started, c.root = true, at
	}
	var key schemaCheckKey
	remember := false
	switch typed := value.(type) {
	case map[string]any:
		remember = len(typed) > 0
	case []any:
		remember = len(typed) > 0
	}
	if remember {
		key = schemaCheckKey{node, reflect.ValueOf(value).Pointer()}
		if fault, judged := c.memo[key]; judged {
			return fault
		}
	}
	fault := c.judge(node, value, noun, at)
	if remember && len(c.memo) < maxSchemaCheckMemo {
		c.memo[key] = fault
	}
	return fault
}

func (c *schemaCheck) judge(node *schemaNode, value any, noun string, at []string) *schemaFault {
	chain := node.chain()
	for _, each := range chain {
		if c.step() {
			return c.tooCostly()
		}
		if fault := each.checkWords(value, at); fault != nil {
			return fault
		}
	}
	switch typed := value.(type) {
	case map[string]any:
		seen := map[string]bool{}
		var names []string
		for name := range typed {
			seen[name] = true
			names = append(names, name)
		}
		for _, each := range chain {
			for _, name := range each.required {
				if !seen[name] {
					seen[name] = true
					names = append(names, name)
				}
			}
		}
		sort.Strings(names)
		for _, name := range names {
			member, present := typed[name]
			memberAt := appendPointer(at, name)
			for _, each := range chain {
				if c.step() {
					return c.tooCostly()
				}
				rule, ruled := each.properties[name]
				switch {
				case !present:
					if containsString(each.required, name) {
						return &schemaFault{memberAt, "required", "the " + noun + " is required"}
					}
				case ruled:
					if fault := c.value(rule, member, "member", memberAt); fault != nil {
						return fault
					}
				case each.additionalFalse:
					return &schemaFault{memberAt, "additionalProperties", "the " + noun + " is not allowed"}
				}
			}
		}
	case []any:
		for index, item := range typed {
			itemAt := appendPointer(at, strconv.Itoa(index))
			for _, each := range chain {
				if c.step() {
					return c.tooCostly()
				}
				if each.items == nil {
					continue
				}
				if fault := c.value(each.items, item, "member", itemAt); fault != nil {
					return fault
				}
			}
		}
	}
	return nil
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// checkWords judges the words of one node that look at the value itself, not
// at its members.
func (n *schemaNode) checkWords(value any, at []string) *schemaFault {
	if len(n.types) > 0 && !schemaTypeMatches(n.types, value) {
		return &schemaFault{at, "type", "the value must be " + describeSchemaTypes(n.types)}
	}
	if n.enum != nil && !schemaValueListed(n.enumValues, value) {
		return &schemaFault{at, "enum", "the value must be one of " + strings.Join(n.enum, ", ")}
	}
	if n.hasConst && !schemaValuesEqual(n.constParsed, value) {
		return &schemaFault{at, "const", "the value must be " + n.constValue}
	}
	switch typed := value.(type) {
	case string:
		length := int64(utf8.RuneCountInString(typed))
		if n.minLength >= 0 && length < n.minLength {
			return &schemaFault{at, "minLength", fmt.Sprintf("the value must be at least %s", countNoun(n.minLength, "character"))}
		}
		if n.maxLength >= 0 && length > n.maxLength {
			return &schemaFault{at, "maxLength", fmt.Sprintf("the value must be at most %s", countNoun(n.maxLength, "character"))}
		}
	case json.Number:
		number, ok := parseRelicNumber(typed.String())
		if !ok {
			break
		}
		if n.minimum != nil && number.compare(*n.minimum) < 0 {
			return &schemaFault{at, "minimum", "the value must be at least " + n.minimumText}
		}
		if n.maximum != nil && number.compare(*n.maximum) > 0 {
			return &schemaFault{at, "maximum", "the value must be at most " + n.maximumText}
		}
	case []any:
		count := int64(len(typed))
		if n.minItems >= 0 && count < n.minItems {
			return &schemaFault{at, "minItems", fmt.Sprintf("the list must have at least %s", countNoun(n.minItems, "item"))}
		}
		if n.maxItems >= 0 && count > n.maxItems {
			return &schemaFault{at, "maxItems", fmt.Sprintf("the list must have at most %s", countNoun(n.maxItems, "item"))}
		}
	}
	return nil
}

func schemaValueListed(list []any, value any) bool {
	for _, item := range list {
		if schemaValuesEqual(item, value) {
			return true
		}
	}
	return false
}

// schemaValuesEqual compares two decoded values as JSON Schema's enum and
// const do: numbers by value, lists in order, objects by name, and anything
// else as written.
func schemaValuesEqual(a, b any) bool {
	switch x := a.(type) {
	case json.Number:
		y, ok := b.(json.Number)
		return ok && schemaNumbersEqual(x.String(), y.String())
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case nil:
		return b == nil
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for index := range x {
			if !schemaValuesEqual(x[index], y[index]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for name, member := range x {
			other, present := y[name]
			if !present || !schemaValuesEqual(member, other) {
				return false
			}
		}
		return true
	}
	return false
}

// schemaNumbersEqual compares two numbers by value. A number whose exponent
// was too large to hold exactly equals only the same text.
func schemaNumbersEqual(a, b string) bool {
	x, okX := parseRelicNumber(a)
	y, okY := parseRelicNumber(b)
	if !okX || !okY || x.clamped || y.clamped {
		return a == b
	}
	return x.compare(y) == 0
}

func countNoun(count int64, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return strconv.FormatInt(count, 10) + " " + noun + "s"
}

func schemaTypeMatches(types []string, value any) bool {
	for _, name := range types {
		switch typed := value.(type) {
		case string:
			if name == "string" {
				return true
			}
		case bool:
			if name == "boolean" {
				return true
			}
		case nil:
			if name == "null" {
				return true
			}
		case map[string]any:
			if name == "object" {
				return true
			}
		case []any:
			if name == "array" {
				return true
			}
		case json.Number:
			if name == "number" {
				return true
			}
			if name == "integer" {
				if number, ok := parseRelicNumber(typed.String()); ok && number.isInteger() {
					return true
				}
			}
		}
	}
	return false
}

func describeSchemaTypes(types []string) string {
	described := make([]string, len(types))
	for index, name := range types {
		switch name {
		case "null":
			described[index] = "null"
		case "integer", "object", "array":
			described[index] = "an " + name
		default:
			described[index] = "a " + name
		}
	}
	return strings.Join(described, " or ")
}

func schemaPointerText(pointer []string) string {
	escaped := make([]string, len(pointer))
	for index, token := range pointer {
		escaped[index] = escapeSchemaPointer(token)
	}
	return "/" + strings.Join(escaped, "/")
}

// memberProblem turns a fault in an object of named members, a relic's
// properties or a part's fields, into a problem: the first step of the
// pointer is the member, and anything deeper is named in the message.
func memberProblem(fault *schemaFault, whole, member string) *RelicSchemaProblem {
	if fault == nil {
		return nil
	}
	if len(fault.pointer) == 0 {
		return &RelicSchemaProblem{Where: whole, Keyword: fault.keyword, Message: fault.message}
	}
	where := member + " " + strconv.Quote(fault.pointer[0])
	message := fault.message
	if len(fault.pointer) > 1 {
		message = "at " + schemaPointerText(fault.pointer[1:]) + ": " + message
	}
	return &RelicSchemaProblem{Where: where, Keyword: fault.keyword, Message: message}
}

func propertyProblem(fault *schemaFault) *RelicSchemaProblem {
	return memberProblem(fault, "properties", "property")
}

func decodeSchemaMembers(members map[string]json.RawMessage) (map[string]any, string) {
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	object := make(map[string]any, len(members))
	for _, name := range names {
		value, ok := decodeSchemaInstance(members[name])
		if !ok {
			return nil, name
		}
		object[name] = value
	}
	return object, ""
}

// CheckRelic judges a whole relic: its properties by name, then its live
// parts in order, each part's fields by name. fields holds each part's fields
// by part ID. Removed parts are not judged.
func (s *RelicSchema) CheckRelic(relic RelicData, fields map[string]map[string]json.RawMessage) *RelicSchemaProblem {
	if s.properties != nil {
		object, bad := decodeSchemaMembers(relic.Properties)
		if bad != "" {
			return &RelicSchemaProblem{Where: "property " + strconv.Quote(bad), Message: "the value is not valid JSON"}
		}
		if problem := propertyProblem(checkValue(s.properties, object, "property", nil)); problem != nil {
			return problem
		}
	}
	for _, part := range relic.Parts {
		if part.Removed {
			continue
		}
		if problem := s.CheckPart("part "+part.ID, part, fields[part.ID]); problem != nil {
			return problem
		}
	}
	return nil
}

// CheckProperty judges one property being set: its rule, or that the schema
// allows no property by that name.
func (s *RelicSchema) CheckProperty(name string, value json.RawMessage) *RelicSchemaProblem {
	if s.properties == nil {
		return nil
	}
	where := "property " + strconv.Quote(name)
	decoded, ok := decodeSchemaInstance(value)
	if !ok {
		return &RelicSchemaProblem{Where: where, Message: "the value is not valid JSON"}
	}
	check := newSchemaCheck(MaxRelicSchemaCheckSteps)
	for _, node := range s.properties.chain() {
		rule, ruled := node.properties[name]
		switch {
		case ruled:
			if problem := propertyProblem(check.value(rule, decoded, "member", []string{name})); problem != nil {
				return problem
			}
		case node.additionalFalse:
			return &RelicSchemaProblem{Where: where, Keyword: "additionalProperties", Message: "the property is not allowed"}
		}
	}
	return nil
}

// CheckPropertyClear judges one property being cleared: a required one
// cannot be.
func (s *RelicSchema) CheckPropertyClear(name string) *RelicSchemaProblem {
	if s.properties == nil {
		return nil
	}
	for _, node := range s.properties.chain() {
		if containsString(node.required, name) {
			return &RelicSchemaProblem{Where: "property " + strconv.Quote(name), Keyword: "required", Message: "the property is required"}
		}
	}
	return nil
}

// CheckPart judges one whole part: that its type is allowed, its fields, its
// body and its body's format. where names the part, as "part <id>" or, for a
// part not yet saved, "part <n>"; the problem's Where adds the part type and,
// for a field or the body, which one. A body with no format counts as txt.
func (s *RelicSchema) CheckPart(where string, part RelicPart, fields map[string]json.RawMessage) *RelicSchemaProblem {
	label := where + " (" + part.Type + ")"
	if s.parts == nil {
		return nil
	}
	rule, ok := s.parts[part.Type]
	if !ok {
		return &RelicSchemaProblem{Where: label, Keyword: "parts", Message: "the schema allows no part of this type"}
	}
	if rule.fields != nil {
		object, bad := decodeSchemaMembers(fields)
		if bad != "" {
			return &RelicSchemaProblem{Where: label + " field " + strconv.Quote(bad), Message: "the value is not valid JSON"}
		}
		if problem := memberProblem(checkValue(rule.fields, object, "field", nil), label, label+" field"); problem != nil {
			return problem
		}
	}
	bodyWhere := label + " body"
	switch {
	case rule.body == relicPartBodyRequired && part.Body == nil:
		return &RelicSchemaProblem{Where: bodyWhere, Keyword: "body", Message: "the part must have a body"}
	case rule.body == relicPartBodyNone && part.Body != nil:
		return &RelicSchemaProblem{Where: bodyWhere, Keyword: "body", Message: "the part must not have a body"}
	}
	if part.Body != nil && rule.bodyFormats != nil {
		format := part.Body.Format
		if format == "" {
			format = "txt"
		}
		if !containsString(rule.bodyFormats, format) {
			allowed := strings.Join(rule.bodyFormats, ", ")
			message := "the body format " + format + " is not one of " + allowed
			if part.Body.Format == "" {
				message = "a body with no format counts as txt, which is not one of " + allowed
			}
			return &RelicSchemaProblem{Where: bodyWhere, Keyword: "bodyFormats", Message: message}
		}
	}
	return nil
}

// relicNumber is a JSON number held exactly: digits × 10^exp, with digits
// carrying no leading or trailing zeros. It is never expanded, so a number
// written with a huge exponent costs no more than its text.
type relicNumber struct {
	sign   int // -1, 0 or 1; 0 means the number is zero and digits is empty
	digits string
	exp    int64
	// clamped is set when the written exponent was past
	// relicNumberExponentLimit, so exp holds the limit, not the exponent.
	clamped bool
}

// relicNumberExponentLimit bounds an exponent as read. Any exponent past it
// compares the same way, since no stored text is long enough to bring a
// number that size back into range.
const relicNumberExponentLimit = 1 << 40

func parseRelicNumber(text string) (relicNumber, bool) {
	index := 0
	negative := false
	if index < len(text) && text[index] == '-' {
		negative = true
		index++
	}
	digitsFrom := func() string {
		start := index
		for index < len(text) && text[index] >= '0' && text[index] <= '9' {
			index++
		}
		return text[start:index]
	}
	whole := digitsFrom()
	if whole == "" {
		return relicNumber{}, false
	}
	fraction := ""
	if index < len(text) && text[index] == '.' {
		index++
		if fraction = digitsFrom(); fraction == "" {
			return relicNumber{}, false
		}
	}
	var exponent int64
	clamped := false
	if index < len(text) && (text[index] == 'e' || text[index] == 'E') {
		index++
		exponentNegative := false
		if index < len(text) && (text[index] == '+' || text[index] == '-') {
			exponentNegative = text[index] == '-'
			index++
		}
		raw := digitsFrom()
		if raw == "" {
			return relicNumber{}, false
		}
		written := strings.TrimLeft(raw, "0")
		if len(written) > 13 {
			exponent, clamped = relicNumberExponentLimit, true
		} else if written != "" {
			exponent, _ = strconv.ParseInt(written, 10, 64)
			if exponent > relicNumberExponentLimit {
				exponent, clamped = relicNumberExponentLimit, true
			}
		}
		if exponentNegative {
			exponent = -exponent
		}
	}
	if index != len(text) {
		return relicNumber{}, false
	}
	digits := strings.TrimLeft(whole+fraction, "0")
	exponent -= int64(len(fraction))
	trimmed := strings.TrimRight(digits, "0")
	exponent += int64(len(digits) - len(trimmed))
	if trimmed == "" {
		return relicNumber{}, true
	}
	number := relicNumber{sign: 1, digits: trimmed, exp: exponent, clamped: clamped}
	if negative {
		number.sign = -1
	}
	return number, true
}

func (n relicNumber) isInteger() bool {
	return n.sign == 0 || n.exp >= 0
}

// clampedInt64 is the number as an int64, or math.MaxInt64 when it is larger.
// It is used for non-negative integers only.
func (n relicNumber) clampedInt64() int64 {
	if n.sign == 0 {
		return 0
	}
	if int64(len(n.digits))+n.exp > 18 {
		return math.MaxInt64
	}
	value, err := strconv.ParseInt(n.digits+strings.Repeat("0", int(n.exp)), 10, 64)
	if err != nil {
		return math.MaxInt64
	}
	return value
}

func (n relicNumber) compare(other relicNumber) int {
	if n.sign != other.sign {
		if n.sign < other.sign {
			return -1
		}
		return 1
	}
	if n.sign == 0 {
		return 0
	}
	magnitude := 0
	leading, otherLeading := int64(len(n.digits))+n.exp, int64(len(other.digits))+other.exp
	switch {
	case leading < otherLeading:
		magnitude = -1
	case leading > otherLeading:
		magnitude = 1
	default:
		magnitude = strings.Compare(n.digits, other.digits)
	}
	return magnitude * n.sign
}

// CheckLinkPointer judges one link's pointer against the schema's
// links.pointer rule, and accepts any pointer when there is no rule. A fault
// inside the pointer is named by its JSON pointer.
func (s *RelicSchema) CheckLinkPointer(pointer json.RawMessage) *RelicSchemaProblem {
	if s.pointer == nil {
		return nil
	}
	const where = "link pointer"
	decoded, ok := decodeSchemaInstance(pointer)
	if !ok {
		return &RelicSchemaProblem{Where: where, Message: "the value is not valid JSON"}
	}
	fault := checkValue(s.pointer, decoded, "member", nil)
	if fault == nil {
		return nil
	}
	message := fault.message
	if len(fault.pointer) > 0 {
		message = "at " + schemaPointerText(fault.pointer) + ": " + message
	}
	return &RelicSchemaProblem{Where: where, Keyword: fault.keyword, Message: message}
}
