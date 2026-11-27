package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// What a relic refuses of JSON text it is given, before it decodes it.
//
// encoding/json reads all of the following without complaint, and each would
// store something the text never said. A byte sequence that is not UTF-8, and
// a \u escape for half of a surrogate pair, each become U+FFFD. Of a member
// named twice in one object the last one wins, or, decoding into a struct,
// the two are merged. And a struct's members are matched without regard to
// letter case, so "Title" is read as "title".

// relicTextScan is what one pass over JSON text finds without decoding it.
type relicTextScan struct {
	// depth is how deep the text nests arrays and objects: 0 for a scalar, 1
	// for [1].
	depth int
	// surrogate is the offset of the first \u escape that is half of a
	// surrogate pair with no other half beside it, or -1.
	surrogate int
}

// scanRelicJSONText makes one pass over the bytes of JSON text. It keeps a
// counter and nothing else, so it costs the same for any depth and can run
// before anything that decodes the text by recursion. It does not judge
// whether the text is valid JSON, which is the decoder's to say; of text that
// is not, it counts the brackets outside strings.
//
// Only this pass can find half a surrogate pair: once the text is decoded,
// the half is a replacement character like any written on purpose.
func scanRelicJSONText(raw []byte) relicTextScan {
	scan := relicTextScan{surrogate: -1}
	depth, inString := 0, false
	for index := 0; index < len(raw); index++ {
		b := raw[index]
		if !inString {
			switch b {
			case '"':
				inString = true
			case '[', '{':
				depth++
				if depth > scan.depth {
					scan.depth = depth
				}
			case ']', '}':
				depth--
			}
			continue
		}
		switch b {
		case '"':
			inString = false
		case '\\':
			escape := index
			index++ // the character a backslash escapes is never a closing quote
			unit, isUnit := relicEscapedUnit(raw, index)
			if !isUnit {
				continue
			}
			index += 4
			half := unit >= 0xDC00 && unit <= 0xDFFF
			if unit >= 0xD800 && unit <= 0xDBFF {
				// A first half is whole only with a second half right after it.
				half = true
				if index+2 < len(raw) && raw[index+1] == '\\' {
					if second, isUnit := relicEscapedUnit(raw, index+2); isUnit && second >= 0xDC00 && second <= 0xDFFF {
						half = false
						index += 6
					}
				}
			}
			if half && scan.surrogate < 0 {
				scan.surrogate = escape
			}
		}
	}
	return scan
}

// relicEscapedUnit reads the u and four hex digits of a \u escape at raw[at:],
// the position after the backslash.
func relicEscapedUnit(raw []byte, at int) (unit uint16, ok bool) {
	if at+5 > len(raw) || raw[at] != 'u' {
		return 0, false
	}
	parsed, err := strconv.ParseUint(string(raw[at+1:at+5]), 16, 16)
	if err != nil {
		return 0, false
	}
	return uint16(parsed), true
}

// relicValueDepth counts how deep a JSON value nests arrays and objects: 0 for
// a scalar, 1 for [1].
func relicValueDepth(raw []byte) int {
	return scanRelicJSONText(raw).depth
}

// firstInvalidUTF8 returns the offset of the first byte that is not part of
// valid UTF-8, or -1.
func firstInvalidUTF8(raw []byte) int {
	if utf8.Valid(raw) {
		return -1
	}
	for index := 0; index < len(raw); {
		r, size := utf8.DecodeRune(raw[index:])
		if r == utf8.RuneError && size == 1 {
			return index
		}
		index += size
	}
	return -1
}

// relicBadText is the first place JSON text holds something that is not text:
// offset is -1 when there is none, and surrogate says which kind it is.
type relicBadText struct {
	offset    int
	surrogate bool
}

func relicBadTextIn(raw []byte, scan relicTextScan) relicBadText {
	bad := relicBadText{offset: firstInvalidUTF8(raw)}
	if scan.surrogate >= 0 && (bad.offset < 0 || scan.surrogate < bad.offset) {
		bad = relicBadText{offset: scan.surrogate, surrogate: true}
	}
	return bad
}

// describe says what is wrong with the text, never repeating it.
func (bad relicBadText) describe(what string) string {
	if bad.surrogate {
		return what + ` has a \u escape for half of a surrogate pair`
	}
	return what + " is not valid UTF-8"
}

// relicJSONPlace says what an object or array is in a workbook.relic
// document, which decides the member names it may have.
type relicJSONPlace int

const (
	// relicJSONFree is anything whose member names are the writer's own:
	// properties, fields, and everything inside a value.
	relicJSONFree relicJSONPlace = iota
	relicJSONDocument
	relicJSONParts
	relicJSONPart
	relicJSONBody
)

// relicDocumentMembers holds, for each place whose members are fixed, the
// names it has and what the place is called. They are read from the struct
// tags the decoder uses, so the two cannot disagree.
var relicDocumentMembers = map[relicJSONPlace]struct {
	names map[string]struct{}
	what  string
}{
	relicJSONDocument: {jsonMemberNames(reflect.TypeOf(RelicDocument{})), "a relic document"},
	relicJSONPart:     {jsonMemberNames(reflect.TypeOf(RelicDocumentPart{})), "a part"},
	relicJSONBody:     {jsonMemberNames(reflect.TypeOf(RelicDocumentBody{})), "a body"},
}

func jsonMemberNames(structure reflect.Type) map[string]struct{} {
	names := make(map[string]struct{}, structure.NumField())
	for index := 0; index < structure.NumField(); index++ {
		field := structure.Field(index)
		tag := field.Tag.Get("json")
		// An embedded struct with no name of its own lends its members to
		// the outer object, as the decoder reads them.
		if field.Anonymous && tag == "" && field.Type.Kind() == reflect.Struct {
			for name := range jsonMemberNames(field.Type) {
				names[name] = struct{}{}
			}
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		names[name] = struct{}{}
	}
	return names
}

// within says what a value is, given the place that holds it and the member
// it is the value of.
func (place relicJSONPlace) within(member string, object bool) relicJSONPlace {
	switch {
	case place == relicJSONDocument && member == "parts" && !object:
		return relicJSONParts
	case place == relicJSONParts && object:
		return relicJSONPart
	case place == relicJSONPart && member == "body" && object:
		return relicJSONBody
	}
	return relicJSONFree
}

// relicJSONFrame is one open object or array while JSON text is walked.
type relicJSONFrame struct {
	object bool
	place  relicJSONPlace
	// An object's names so far, the member whose value is being read, and
	// whether one is; an array's index of the element being read.
	names   map[string]struct{}
	member  string
	inValue bool
	index   int
}

// relicJSONPathLimit is the longest path a refusal gives. A longer one is cut
// short, so a message does not grow with how deep the text nests.
const relicJSONPathLimit = 120

// relicJSONPath names where the walk is, as parts[1].fields.heading. It is
// empty at the top. Indexes count from 0. A name that is not plain letters,
// digits, '_' and '-' is quoted in brackets.
func relicJSONPath(stack []relicJSONFrame) string {
	var path strings.Builder
	for _, frame := range stack {
		var segment string
		switch {
		case !frame.object:
			segment = "[" + strconv.Itoa(frame.index) + "]"
		case !frame.inValue:
			continue
		case len(frame.member) > relicMessageNameLimit:
			segment = fmt.Sprintf("[a name %d bytes long]", len(frame.member))
		case relicPlainMemberName(frame.member):
			segment = frame.member
			if path.Len() > 0 {
				segment = "." + segment
			}
		default:
			segment = "[" + strconv.Quote(frame.member) + "]"
		}
		if path.Len()+len(segment) > relicJSONPathLimit {
			path.WriteString("...")
			break
		}
		path.WriteString(segment)
	}
	return path.String()
}

func relicPlainMemberName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		plain := r == '_' || r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !plain {
			return false
		}
	}
	return true
}

// relicJSONWhere turns a path into the end of a sentence: " in <path>", or
// nothing at the top.
func relicJSONWhere(preposition, path string) string {
	if path == "" {
		return ""
	}
	return " " + preposition + " " + path
}

// checkRelicJSON walks JSON text token by token and refuses, as validation,
// the first of these it meets: text that is not text, at bad; an object that
// names a member twice, at any depth; and, when root is relicJSONDocument, a
// member a relic document, a part or a body does not have, matched exactly.
// what names the text, as in "the value".
//
// Text that is not valid JSON is left to the decoder that reads it next, so
// that there is one wording for it. The walk keeps its own stack and does not
// recurse.
func checkRelicJSON(raw []byte, what string, root relicJSONPlace, bad relicBadText) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var stack []relicJSONFrame
	// unknown is a member the document itself does not have, held back until
	// its other members are known: a result envelope is told apart by two.
	var unknown error
	var topNames map[string]struct{}
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		// The token that ends past the bad offset is the one that holds it.
		holdsBad := bad.offset >= 0 && decoder.InputOffset() > int64(bad.offset)

		var top *relicJSONFrame
		if len(stack) > 0 {
			top = &stack[len(stack)-1]
		}
		if name, isName := token.(string); isName && top != nil && top.object && !top.inValue {
			if unknown != nil {
				if len(stack) == 1 {
					topNames[name] = struct{}{}
				}
				top.member, top.inValue = name, true
				continue
			}
			path := relicJSONPath(stack)
			if holdsBad {
				return Errorf(CategoryValidation, "%s at a member name%s",
					bad.describe(what), relicJSONWhere("in", path))
			}
			if _, repeated := top.names[name]; repeated {
				return Errorf(CategoryValidation, "%s names %s twice%s; an object names a member once",
					what, relicNamedInMessage("member", name), relicJSONWhere("in", path))
			}
			top.names[name] = struct{}{}
			if members, fixed := relicDocumentMembers[top.place]; fixed {
				if _, known := members.names[name]; !known {
					refusal := relicUnknownMember(what, name, members.what, members.names, path)
					if len(stack) > 1 {
						return refusal
					}
					unknown, topNames = refusal, top.names
				}
			}
			top.member, top.inValue = name, true
			continue
		}

		if delimiter, isDelimiter := token.(json.Delim); isDelimiter {
			switch delimiter {
			case '{', '[':
				frame := relicJSONFrame{object: delimiter == '{', place: root}
				if top != nil {
					frame.place = top.place.within(top.member, frame.object)
				}
				if frame.object {
					frame.names = map[string]struct{}{}
				}
				stack = append(stack, frame)
				continue
			default:
				stack = stack[:len(stack)-1]
				if len(stack) > 0 {
					top = &stack[len(stack)-1]
				} else {
					top = nil
				}
			}
		} else if holdsBad && unknown == nil {
			path := relicJSONPath(stack)
			message := bad.describe(what) + relicJSONWhere("at", path)
			if top != nil && top.place == relicJSONBody && top.member == "text" {
				message += `; a body that is not UTF-8 text goes in the body's "base64" member`
			}
			return Errorf(CategoryValidation, "%s", message)
		}
		// A value has ended: a scalar, or an object or array just closed.
		if top != nil {
			if top.object {
				top.inValue = false
			} else {
				top.index++
			}
		}
	}

	if unknown != nil {
		_, hasCommand := topNames["command"]
		_, hasData := topNames["data"]
		if hasCommand && hasData {
			return Errorf(CategoryValidation,
				`this is the JSON result of a command, not a relic document: it has "command" and "data" members; `+
					`"workbook relic export <relic>" without --json writes the document, and the document is the result's "data" member`)
		}
		return unknown
	}
	if bad.offset >= 0 {
		// The bad text sits where no token holds it, so the text is not JSON
		// either; it is refused for what is certain about it.
		return Errorf(CategoryValidation, "%s", bad.describe(what))
	}
	return nil
}

// relicUnknownMember refuses a member a place in a relic document does not
// have. A name that differs from one it has only by letter case is the likely
// slip, so the refusal gives the spelling.
func relicUnknownMember(what, name, place string, names map[string]struct{}, path string) error {
	message := fmt.Sprintf("%s has %s%s, which %s does not have",
		what, relicNamedInMessage("member", name), relicJSONWhere("in", path), place)
	for known := range names {
		if strings.EqualFold(known, name) {
			message += fmt.Sprintf("; member names are matched exactly, and this one is written %q", known)
			break
		}
	}
	return Errorf(CategoryValidation, "%s", message)
}

// checkRelicValueText refuses a value that is not text all the way through or
// that names a member twice in one object. It runs before the value is
// decoded, for a value given to a command and for one read from storage
// alike; no value in its stored form is refused by it, because the stored
// form is what encoding the decoded value gives, which holds neither.
func checkRelicValueText(raw []byte, scan relicTextScan) error {
	bad := relicBadTextIn(raw, scan)
	if bad.offset >= 0 {
		return Errorf(CategoryValidation, "%s", bad.describe("the value"))
	}
	// Only an object has members to repeat.
	if bytes.IndexByte(raw, '{') < 0 {
		return nil
	}
	return checkRelicJSON(raw, "the value", relicJSONFree, bad)
}

// checkRelicDocumentText refuses a workbook.relic document that is not text
// all the way through, that names a member twice in one object at any depth,
// or that has a member the format does not, with names matched exactly.
func checkRelicDocumentText(raw []byte) error {
	return checkRelicJSON(raw, "the relic document", relicJSONDocument, relicBadTextIn(raw, scanRelicJSONText(raw)))
}
