module github.com/polynetwork/ont-relayer

go 1.14

require (
	github.com/boltdb/bolt v1.3.1
	github.com/ontio/ontology v1.12.0
	github.com/ontio/ontology-go-sdk v1.11.4
	github.com/polynetwork/bsc-relayer v0.0.0-00010101000000-000000000000
	github.com/polynetwork/poly v0.0.0-20210108071928-86193b89e4e0
	github.com/polynetwork/poly-go-sdk v0.0.0-20200817120957-365691ad3493
	github.com/stretchr/testify v1.6.1
	github.com/urfave/cli v1.22.5
	github.com/polynetwork/poly-bridge/bridgesdk v0.0.2
)

replace poly-bridge => github.com/polynetwork/poly-bridge v0.0.0-20210126083254-80335b53070a

replace github.com/polynetwork/bsc-relayer => github.com/zhiqiangxu/bsc-relayer v0.0.0-20210225063107-648c654c340f
