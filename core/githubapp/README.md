# GitHub App 鉴权验证

`AppAuth` 的 installation token 缓存按 installation ID 隔离。缓存仅在剩余
有效期大于 60 秒时命中；恰好 60 秒或更短时刷新。无效 ID、空 token、含空白
或控制字符的 token、无效或已过期的 `expires_at`、HTTP 失败和取消均返回错误，
不会写入新的缓存记录。Token 按不透明凭证处理，不假设固定长度或前缀。

JWT 使用 RS256，`iat` 为当前时间减 60 秒，`exp` 为当前时间加 600 秒。
GitHub 的十分钟上限相对于当前时间，并非回拨后的 `exp-iat`。
参见 [GitHub 的 JWT 要求](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app)。

## 已完成的本地验证

`core/githubapp/zz_auth_protocol_test.go` 使用运行时生成的临时 RSA 密钥、
可控时钟和 localhost HTTP 服务。验证器使用独立的 `rsa.VerifyPKCS1v15`，
检查签名与 claims，并拒绝篡改及失效 JWT。HTTP 路径覆盖实际 `AppAuth`
签发请求、缓存命中、提前刷新、跨 installation 隔离、并发单次刷新及失败响应。
这些测试不需要生产凭证，也不向 GitHub 或模型供应商发出请求。

```bash
go test -race -count=1 ./core/githubapp
go test -race -count=1 ./...
make verify
```

本次修改的最终源码通过全仓 race 测试和 `make verify`；CI 会继续执行同一套门禁。

## 尚未验证的集成边界

本地协议测试不证明生产 App 注册、私钥、installation、仓库范围和权限配置正确，
也不证明 GitHub 已接受生产 JWT 或完成真实 token 到期刷新。本次没有可用生产
私钥，未执行这些线上验证。

刷新仍在全局互斥锁内进行。已取消的等待者须等当前刷新释放锁，之后会在命中
缓存或发送请求前返回取消错误；本次不提供即时取消等待的保证。按 installation
隔离缓存也不等于上层支持多 installation 路由：daemon 和 MCP 现有的首次仓库
或 catalog 选择策略未改动。
