# QA / E2E 经验

## 本地起一个 OpenList 做端到端验证

1. **前端资源缺失会直接 FATA**

   `go build -o /tmp/olrun/openlist .` 出来的二进制里嵌的是 `public/dist`，仓库里没有这个目录，启动时会：
   ```text
   FATA index.html not exist, you may forget to put dist of frontend to public/dist
   ```
   不想构建前端时，写 `data/config.json` 指定 `dist_dir` 指向一个只含 `index.html` 的目录即可（API 能正常用，UI 打不开）：
   ```json
   { "dist_dir": "/tmp/olrun/fakedist" }
   ```

2. **首次启动没有管理员账号**

   `openlist admin set <密码>` 只对已存在的 admin 生效（日志里是 `failed get admin user: record not found`）。新库要走初始化接口：
   ```
   GET  /api/public/init_status      → {"initialized": false}
   POST /api/public/init/setup       {"username":"admin","password":"1234","site_title":"x"}
   POST /api/auth/login              → data.token，后续请求带 Authorization
   ```

3. **后台进程会被回收**

   在工具里用 `cmd &` / `nohup` 起的进程，等这条命令返回后往往就没了。要把「启动服务 + 跑测试 + kill」写进同一条命令里；跨命令用 `run_in_background`。

4. **`pkill -f "openlist server"` 会杀掉自己**

   匹配模式命中了正在执行这条命令的 shell 命令行本身（导致整个命令 exit 144）。改用 `for p in $(pgrep -x openlist); do kill $p; done`。

## QQ 群文件（OneBot）相关

- 测试用机器人自己建的群，跑完测试记得把目录和文件删干净（`get_group_root_files` 应返回空）。
- `11.11.1.13:8398` 上跑的是 LLBot 7.12.15：`get_group_files_by_folder` 只返回 files，**不返回子目录**（NapCat 会返回），所以群文件在 LLBot 下实际只能看到一层。
- 根目录在 API 里的表示不统一：`get_group_files_by_folder` 的 `folder_id` 为空或 `/` 都可以；但 `move_group_file` 的 `target_directory` 必须是 `/`（传空字符串会报「请稍候重试」）。
- `upload_group_file` 的 `folder` 传空字符串就是根目录；body 里的 `file` 用 `base64://`，接口接受 chunked 请求体（可以流式发，不用先在内存里编码）。

## 本地构建镜像（build-image.sh）

- **前端本地 `pnpm build` 出来只有英文**：`OpenList-Frontend/src/lang` 除 `en/` 外全被 gitignore（翻译在 Crowdin 上），CI 是跑 `pnpm i18n:release` 下载后再构建的。官方 release（`edge` tag）里附了 `i18n.tar.gz`，内有 `zh-CN`、`zh-TW` 两个语言，解压进 `src/lang` 再 `node scripts/i18n.mjs` + `pnpm build` 就有中文了。
- 仓库根目录的 `Dockerfile` 会跑 `build.sh release docker`，从官方 release 下 dist 覆盖 `public/dist`，本地前端改动会丢；本地出镜像应该自己 `go build` 后单独打包镜像。
- `public/dist/README.md` 是 `public/dist` 下唯一被 git 跟踪的文件，`rm -rf public/dist` 之后记得 `git checkout -- public/dist/README.md`。
- 官方镜像起容器必须 `-v xxx:/opt/openlist/data --user 0:0`（compose 里就是这样），否则会报 data 目录没有写权限并退出。
