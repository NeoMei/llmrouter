package volcengine

import (
	"slices"

	"github.com/QuantumNous/new-api/relay/channel/task/doubao"
)

var ModelList = slices.Concat([]string{
	"Doubao-pro-128k",
	"Doubao-pro-32k",
	"Doubao-pro-4k",
	"Doubao-lite-128k",
	"Doubao-lite-32k",
	"Doubao-lite-4k",
	"Doubao-embedding",
	"doubao-seedream-4-0-250828",
	"doubao-seedream-4-0-20260415",
	"seedream-4-0-250828",
	"doubao-seedream-4-5-251128",
	"doubao-seedream-5-0-260128",
	"doubao-seedream-5-0-lite-260128",
	"doubao-seedream-5-0-flash-260915",
	"doubao-seedream-5-0-pro-260628",
	"doubao-seedream-5-5-261028",
	"doubao-seed-evolving",
	"doubao-seed-2-1-pro-260915",
	"doubao-seed-2-1-lite-260915",
	"doubao-seed-2-1-turbo-260628",
	"doubao-seed-2-0-lite-260428",
	"doubao-seed-2-0-mini-260428",
	"seedance-1-0-pro-250528",
	"doubao-seed-1-6-thinking-250715",
	"seed-1-6-thinking-250715",
}, doubao.ModelList)

var ChannelName = "volcengine"
