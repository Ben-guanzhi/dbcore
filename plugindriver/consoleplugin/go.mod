module github.com/dbcore/dbcore/plugindriver/consoleplugin

go 1.25.0

require (
	github.com/dbcore/dbcore v0.0.0
	github.com/segmentio/kafka-go v0.4.51
)

require (
	github.com/klauspost/compress v1.15.9 // indirect
	github.com/pierrec/lz4/v4 v4.1.15 // indirect
)

// The console plugin lives inside the dbcore repository but is a SEPARATE module on
// purpose: its Kafka client dependency must not leak into the kernel that fynequery,
// sqltui and polydb all build against. The replace points at the local kernel so the
// plugin always tracks the checked-out protocol.
replace github.com/dbcore/dbcore => ../../
