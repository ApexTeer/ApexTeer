# 面板前端产物 / Panel bundle

这个目录由 `scripts/fetch-panel.sh` 从 EasySB-Panel 的 Release 填充，`go:embed all:web`
把内容打进二进制。前端由 EasySB-Panel 的 Actions 构建，编译产物不进版本库；这里只留
`README.md` 与占位 `index.html`，让 `go build` 和 `make check` 在没有取过前端时也能通过
编译与测试。取过前端后，占位 `index.html` 会被真实产物覆盖。

This directory is filled by `scripts/fetch-panel.sh` from an EasySB-Panel release and is
embedded into the binary by `go:embed all:web`. The front end is built by EasySB-Panel's
Actions and the built SPA is never committed; only `README.md` and a placeholder
`index.html` live here, so `go build` and `make check` work before any bundle has been
fetched. Once a bundle is fetched the placeholder `index.html` is overwritten.
