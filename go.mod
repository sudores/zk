module github.com/zk-org/zk

go 1.25.0

require (
	github.com/AlecAivazis/survey/v2 v2.3.4
	github.com/alecthomas/kong v0.5.0
	github.com/aws/aws-sdk-go-v2 v1.47.5
	github.com/aws/aws-sdk-go-v2/config v1.33.13
	github.com/aws/aws-sdk-go-v2/service/s3 v1.114.6
	github.com/aws/smithy-go v1.28.7
	github.com/aymerick/raymond v2.0.2+incompatible
	github.com/bmatcuk/doublestar/v4 v4.0.2
	github.com/fatih/color v1.13.0
	github.com/google/go-cmp v0.5.8
	github.com/gosimple/slug v1.12.0
	github.com/kballard/go-shellquote v0.0.0-20180428030007-95032a82bc51
	github.com/lestrrat-go/strftime v1.1.1
	github.com/mattn/go-isatty v0.0.14
	github.com/mattn/go-sqlite3 v1.14.22
	github.com/mvdan/xurls v1.1.0
	github.com/pelletier/go-toml v1.9.5
	github.com/relvacode/iso8601 v1.1.0
	github.com/rvflash/elapsed v0.2.0
	github.com/schollz/progressbar/v3 v3.8.6
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.0.9
	github.com/tj/go-naturaldate v1.3.0
	github.com/tliron/glsp v0.1.1
	github.com/tliron/kutil v0.1.59
	github.com/yuin/goldmark v1.8.1
	github.com/yuin/goldmark-meta v1.1.0
	github.com/zk-org/pretty v0.2.4
	golang.org/x/sync v0.20.0
	golang.org/x/term v0.43.0
	gopkg.in/djherbis/times.v1 v1.3.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.25 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.20.13 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.20.5 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.8 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.8 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.8 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.23 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.9 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.8 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.8 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.12.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.39.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.44.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.51.6 // indirect
	github.com/gorilla/websocket v1.5.0 // indirect
	github.com/gosimple/unidecode v1.0.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/kr/pretty v0.3.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mattn/go-colorable v0.1.12 // indirect
	github.com/mattn/go-runewidth v0.0.13 // indirect
	github.com/mgutz/ansi v0.0.0-20200706080929-d51e80ef957d // indirect
	github.com/mitchellh/colorstring v0.0.0-20190213212951-d06e56a500db // indirect
	github.com/petermattis/goid v0.0.0-20220526132513-07eaf5d0b9f4 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/rivo/uniseg v0.2.0 // indirect
	github.com/rogpeppe/go-internal v1.9.0 // indirect
	github.com/sasha-s/go-deadlock v0.3.1 // indirect
	github.com/sourcegraph/jsonrpc2 v0.1.0 // indirect
	github.com/zchee/color/v2 v2.0.6 // indirect
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
)

// See: https://github.com/zk-org/zk/issues/603
replace github.com/tliron/kutil => github.com/tliron/go-kutil v0.1.59
