module github.com/lesomnus/roster

go 1.27.0

// The generators, as tools of this module.
//
// They are pinned here rather than by payday because a plugin is a Go program
// built from **this** module's graph -- so its version is this app's to choose,
// and `pd doctor` is what says when one is missing rather than `buf generate`
// failing over an executable nobody has heard of.
//
// `go mod tidy` fills in the requires. Nothing is pinned in this file on
// purpose: a template that carried version numbers would be a template that is
// out of date the week after it was written.
tool (
	github.com/lesomnus/payday/cmd/pd
	github.com/lesomnus/payday/cmd/protoc-gen-pd
	github.com/protobuf-orm/ent/cmd/ent
	github.com/protobuf-orm/protobuf-merge
	github.com/protobuf-orm/protoc-gen-orm-ent
	github.com/protobuf-orm/protoc-gen-orm-go
	github.com/protobuf-orm/protoc-gen-orm-service
	google.golang.org/grpc/cmd/protoc-gen-go-grpc
	google.golang.org/protobuf/cmd/protoc-gen-go
)

require (
	github.com/coreos/go-oidc/v3 v3.20.0
	github.com/fxamacker/cbor/v2 v2.9.2
	github.com/go-jose/go-jose/v4 v4.1.4
	github.com/go-webauthn/webauthn v0.17.4
	github.com/google/uuid v1.6.0
	github.com/lesomnus/grpc-dgram v0.0.0-20260808164022-d993065403e1
	github.com/lesomnus/otx v0.0.0-20260807173743-977a5687d6ba
	github.com/lesomnus/payday v0.0.0-20260901170642-6c5ccefa22d2
	github.com/lesomnus/protobuf-patch v0.0.0-20260803175157-e1b7a0c2804f
	github.com/lesomnus/xli v0.0.0-20260717171524-bf8cac633057
	github.com/lesomnus/z v0.0.0-20260531102454-3f1853bb4278
	github.com/protobuf-orm/ent v0.0.0-20260901165429-fc2386a28455
	github.com/protobuf-orm/protobuf-orm v0.0.0-20260901155106-6bf45a2a1e67
	github.com/protobuf-orm/protoc-gen-orm-ent/runtime v0.0.0-20260901170510-1b594b46819a
	github.com/stretchr/testify v1.11.1
	go.opentelemetry.io/otel/trace v1.45.0
	golang.org/x/crypto v0.55.0
	golang.org/x/oauth2 v0.36.0
	golang.org/x/sync v0.22.0
	google.golang.org/grpc v1.83.0
	google.golang.org/protobuf v1.36.12
)

require (
	ariga.io/atlas v1.3.0 // indirect
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.11-20260709200747-435963d16310.1 // indirect
	connectrpc.com/connect v1.20.0 // indirect
	connectrpc.com/cors v0.1.0 // indirect
	connectrpc.com/vanguard v0.4.0 // indirect
	github.com/agext/levenshtein v1.2.3 // indirect
	github.com/apparentlymart/go-textseg/v15 v15.0.0 // indirect
	github.com/apparentlymart/go-textseg/v17 v17.0.1 // indirect
	github.com/bmatcuk/doublestar v1.3.4 // indirect
	github.com/bufbuild/protocompile v0.14.2-0.20260716165721-bb5762d29672 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/ettle/strcase v0.2.0 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-openapi/inflect v1.0.0 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/go-webauthn/x v0.2.6 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/hashicorp/hcl/v2 v2.24.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.10.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/lesomnus/mkot v0.0.0-20260801183340-9c83100aa7c2 // indirect
	github.com/lesomnus/mkot/mkotx v0.0.0-20260801183340-9c83100aa7c2 // indirect
	github.com/lesomnus/mkot/pretty v0.0.0-20260801183340-9c83100aa7c2 // indirect
	github.com/lesomnus/otx/otxgrpc v0.0.0-20260807173743-977a5687d6ba // indirect
	github.com/lesomnus/sqlite3-wasm v0.0.0-20260726134538-bebcaebf933e // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.23 // indirect
	github.com/mitchellh/go-wordwrap v1.0.1 // indirect
	github.com/ncruces/go-sqlite3 v0.35.3 // indirect
	github.com/ncruces/go-sqlite3-wasm/v3 v3.2.35304 // indirect
	github.com/ncruces/julianday v1.0.0 // indirect
	github.com/petermattis/goid v0.0.0-20260716134002-a9b348f0a2b9 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/protobuf-orm/protobuf-merge v0.0.0-20260901155249-39cbf4c6c20f // indirect
	github.com/protobuf-orm/protoc-gen-orm-ent v0.0.0-20260901170510-1b594b46819a // indirect
	github.com/protobuf-orm/protoc-gen-orm-go v0.0.0-20260901155244-4237381a6615 // indirect
	github.com/protobuf-orm/protoc-gen-orm-service v0.0.0-20260901155246-12064a5a7fa8 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/tidwall/btree v1.8.1 // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/zclconf/go-cty v1.19.0 // indirect
	github.com/zclconf/go-cty-yaml v1.2.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/bridges/otelslog v0.18.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.68.0 // indirect
	go.opentelemetry.io/otel v1.45.0 // indirect
	go.opentelemetry.io/otel/log v0.20.0 // indirect
	go.opentelemetry.io/otel/metric v1.45.0 // indirect
	go.opentelemetry.io/otel/sdk v1.44.0 // indirect
	go.opentelemetry.io/otel/sdk/log v0.20.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.44.0 // indirect
	golang.org/x/exp v0.0.0-20260709172345-9ea1abe57597 // indirect
	golang.org/x/mod v0.40.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	golang.org/x/tools v0.49.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260715232425-e75dac1f907d // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260724162435-b2f20204f0df // indirect
	google.golang.org/grpc/cmd/protoc-gen-go-grpc v1.6.2 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
