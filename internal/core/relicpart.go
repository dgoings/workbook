package core

import "context"

// RelicPartView is one part of a relic read by itself: the relic's ID, the head
// it was read at, and the part with its fields and body.
type RelicPartView struct {
	Relic string            `json:"relic"`
	Head  string            `json:"head"`
	Part  RelicDocumentPart `json:"part"`
}

// ShowPart reads one part, addressed as <relic>#<part>, either of them a
// prefix, removed or not. It reads that part's fields and body and nothing
// else: unlike Show it marks no link target, reads no task and does not judge
// the relic against its schema, so its cost does not grow with the other parts.
func (s RelicService) ShowPart(ctx context.Context, address string) (RelicPartView, error) {
	relicPrefix, partPrefix, hasPart, err := ParsePartAddress(address)
	if err != nil {
		return RelicPartView{}, err
	}
	if !hasPart {
		return RelicPartView{}, Errorf(CategoryInvocation, "a part address is <relic>#<part>")
	}
	snapshot, err := s.getRelic(ctx, relicPrefix)
	if err != nil {
		return RelicPartView{}, err
	}
	relicID := snapshot.Record.State.RelicID
	part, err := resolvePartPrefix(relicID, snapshot.Record.State.Relic.Parts, partPrefix)
	if err != nil {
		return RelicPartView{}, err
	}
	content, err := s.Reader.ReadRelicParts(ctx, s.Config, snapshot, []string{part.ID}, true)
	if err != nil {
		return RelicPartView{}, err
	}
	return RelicPartView{Relic: relicID, Head: snapshot.Head, Part: newRelicDocumentPart(part, content[part.ID])}, nil
}
