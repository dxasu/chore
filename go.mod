module github.com/dxasu/chore

go 1.21

require (
	github.com/atotto/clipboard v0.1.4
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.29.1
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092757-25821a8e3504 // indirect
	golang.org/x/sys v0.17.0 // indirect
	modernc.org/gc/v3 v3.0.0-20240107210532-573471604cb6 // indirect
	modernc.org/libc v1.41.0 // indirect
	modernc.org/mathutil v1.6.0 // indirect
	modernc.org/memory v1.7.2 // indirect
	modernc.org/strutil v1.2.0 // indirect
	modernc.org/token v1.1.0 // indirect
)

// bigfft 原 revision 25821a8e3504 已被上游仓库移除（历史重写），用仍存在的版本替代
replace github.com/remyoudompheng/bigfft => github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec
