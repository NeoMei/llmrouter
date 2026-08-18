package toolcallid

import core "github.com/QuantumNous/new-api/internal/toolcallid"

type Normalizer = core.Normalizer

func New(prefix string) *Normalizer {
	return core.New(prefix)
}

func ParseObjectArguments(arguments string) (map[string]any, error) {
	return core.ParseObjectArguments(arguments)
}
