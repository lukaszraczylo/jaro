module jarobench

go 1.25.0

require (
	github.com/fxamacker/cbor/v2 v2.9.6
	github.com/goccy/go-json v0.11.2
	github.com/klauspost/compress v1.19.2
	github.com/lukaszraczylo/jaro v0.0.0
	github.com/ugorji/go/codec v1.3.2
	github.com/vmihailenco/msgpack/v5 v5.4.1
	go.mongodb.org/mongo-driver/v2 v2.9.2
	google.golang.org/protobuf v1.36.12
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	github.com/x448/float16 v0.8.4 // indirect
)

replace github.com/lukaszraczylo/jaro => ../
