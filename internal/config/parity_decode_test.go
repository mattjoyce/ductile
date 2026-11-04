package config

// The decode half of loadAshlar (card 153 slice 3): the dedicated-domain strip
// and the decision-C emulation that collects and prunes unknown keys.

import (
	"errors"
	"fmt"

	"github.com/mattjoyce/ashlar"
	"github.com/mattjoyce/ashlar/yamlconfig"
)

// maxUnknownPrunes bounds the decision-C emulation loop: each pass removes one
// key, so a document cannot need more passes than it has keys.
const maxUnknownPrunes = 10000

// withoutDedicatedDomains drops the top-level keys Config loads outside the
// struct decode (dedicatedScopeDomains, today only "pipelines"), as the strict
// decode warning pass already skips them.
func withoutDedicatedDomains(root ashlar.Value) ashlar.Value {
	if root.Kind() != ashlar.KindObject {
		return root
	}
	var kept []ashlar.Member
	for _, m := range root.Members() {
		if !dedicatedScopeDomains[m.Key] {
			kept = append(kept, m)
		}
	}
	out, err := ashlar.ObjectValue(kept, root.Provenance())
	if err != nil {
		// Dropping members from a valid object cannot create a duplicate key.
		return root
	}
	return out
}

// decodeCollectingUnknown is the decision-C emulation: decode strictly, and on
// each unknown key record its pointer, prune it and decode again. Any other
// decode failure is returned as is.
func decodeCollectingUnknown(root ashlar.Value) (*Config, []string, error) {
	var unknown []string
	for range maxUnknownPrunes {
		var cfg Config
		err := yamlconfig.DecodeValueStrict(root, &cfg)
		if err == nil {
			return &cfg, unknown, nil
		}
		var aerr *ashlar.Error
		if !errors.Is(err, ashlar.ErrUnknownField) || !errors.As(err, &aerr) {
			return nil, unknown, err
		}
		unknown = append(unknown, string(aerr.Pointer))
		if root, err = removeAt(root, aerr.Pointer); err != nil {
			return nil, unknown, err
		}
	}
	return nil, unknown, fmt.Errorf("decode: more than %d unknown keys", maxUnknownPrunes)
}

// removeAt returns val without the member or element at ptr.
func removeAt(val ashlar.Value, ptr ashlar.Pointer) (ashlar.Value, error) {
	tokens, err := ptr.Tokens()
	if err != nil {
		return ashlar.Value{}, err
	}
	if len(tokens) == 0 {
		return ashlar.Value{}, fmt.Errorf("cannot remove the document root")
	}
	return removeTokens(val, tokens)
}

func removeTokens(val ashlar.Value, tokens []string) (ashlar.Value, error) {
	head, rest := tokens[0], tokens[1:]
	switch val.Kind() {
	case ashlar.KindObject:
		members := val.Members()
		kept := make([]ashlar.Member, 0, len(members))
		for _, m := range members {
			switch {
			case m.Key != head:
				kept = append(kept, m)
			case len(rest) > 0:
				child, err := removeTokens(m.Value, rest)
				if err != nil {
					return ashlar.Value{}, err
				}
				m.Value = child
				kept = append(kept, m)
			}
		}
		return ashlar.ObjectValue(kept, val.Provenance())
	case ashlar.KindArray:
		idx, ok := ashlar.ParseArrayIndex(head)
		elems := val.Elements()
		if !ok || idx >= len(elems) || len(rest) == 0 {
			return ashlar.Value{}, fmt.Errorf("cannot remove array element %q", head)
		}
		child, err := removeTokens(elems[idx], rest)
		if err != nil {
			return ashlar.Value{}, err
		}
		elems[idx] = child
		return ashlar.ArrayValue(elems, val.Provenance()), nil
	default:
		return ashlar.Value{}, fmt.Errorf("cannot descend into %s at %q", val.Kind(), head)
	}
}
