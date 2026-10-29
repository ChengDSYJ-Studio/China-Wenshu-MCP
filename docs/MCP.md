# MCP 工具参考

所有工具经官方 Go SDK 发布在 stdio 传输上。`query` 与 CLI 查询 JSON 相同；`job_id` 为任务创建后返回的 UUID。

| 工具 | 主要参数 | 结果 |
|---|---|---|
| `wenshu_session_status` | `check?` | 会话配置与可选在线检查，不返回 Cookie |
| `wenshu_auth_import` | `path` | 导入本地会话文件 |
| `wenshu_login` | `chrome?` | 异步打开登录浏览器；状态见 session_status |
| `wenshu_search` | `query,page?,page_size?` | 一页元数据及总数，默认 5 条 |
| `wenshu_facets` | `query,field` | 分类计数 |
| `wenshu_dictionary` | `key?` | 字典名或该字典内容 |
| `wenshu_document` | `doc_id,fetch?,force?` | 本地文书和段落，或 HTTP 获取并入库 |
| `wenshu_task_create` | `name,query,fetch_body,page_size?,split_fields?` | 创建任务，不自动开始 |
| `wenshu_task_start` | `job_id,max_pages?,request_interval_ms?` | 后台执行/恢复，立即返回 |
| `wenshu_task_pause` | `job_id` | 请求暂停并保留断点 |
| `wenshu_task_status` | `job_id` | 当前状态和数量 |
| `wenshu_task_list` | 无 | 所有本地任务 |
| `wenshu_task_shards` | `job_id` | 分片条件、计数、断点及原因 |
| `wenshu_corpus_search` | `job_id?,keyword?,limit?,offset?` | 本地检索，默认 20 条，最多 10000 |
| `wenshu_link_references` | `text,lookup_missing?,job_id?,max_lookups?` | 文书标题与案号的 Markdown 来源链接 |
| `wenshu_codebook_import` | `path` | 导入 JSON/YAML codebook |
| `wenshu_codebook_list` | 无 | 已导入 codebook |
| `wenshu_code_apply` | `job_id?,book,version,coder?` | 规则编码计数与冲突列表 |
| `wenshu_code_put` | 见人工编码样例 | 保存人工编码，含证据或说明 |
| `wenshu_codings` | `job_id?,book?,version?,coder?,limit?,offset?` | 编码结果和证据，默认100条 |
| `wenshu_export` | `directory,job_id?,book?,version?,coder?,format?` | 导出目录及文件列表 |
| `wenshu_methods` | `job_id?,path?` | Markdown 方法附录；可写入文件 |

创建任务示例：

```json
{
  "name": "2020 年未成年人相关文书",
  "query": {"keyword": "未成年人", "start_date": "2020-01-01", "end_date": "2020-12-31"},
  "fetch_body": true,
  "page_size": 5
}
```

调用 `task_start` 后用 `task_status` 读取真正的运行结果，不把启动返回的 `accepted` 当成采集完成。任务开始后可以继续查看状态和本地语料。

工具通过结构化结果返回数据与状态。任务进度保存在本地语料库，更新会话后可继续采集；文书和 codebook 用于研究数据处理。
