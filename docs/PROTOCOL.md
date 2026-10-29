# HTTP 协议与数据处理说明

## 参考接口

服务依据网站前端接口组织检索、正文获取和会话请求。

- [网站请求参数脚本](https://wenshu.court.gov.cn/website/wenshu/js/strToBinary.js)
- [列表页代码](https://wenshu.court.gov.cn/website/wenshu/181217BMTKHNT2W0/index.js)
- [正文页代码](https://wenshu.court.gov.cn/website/wenshu/181107ANFZ0BXSK4/index.js)
- [网站字段及字典](https://wenshu.court.gov.cn/website/wenshu/js/wenshulist1.js)
- [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk)
- [Stata 数据格式](https://www.stata.com/help.cgi?dta)、[pandas Stata 读写实现](https://github.com/pandas-dev/pandas/blob/main/pandas/io/stata.py)

## 请求

`POST https://wenshu.court.gov.cn/website/parse/rest.q4w`，表单编码。

`cfg=com.lawyee.judge.dc.parse.dto.SearchDataDsoDTO@操作名`：

- `queryDoc`：`queryCondition`、`sortFields`、`pageNum`、`pageSize`。
- `leftDataItem`：`queryCondition`、`groupFields`。
- `docInfoSearch`：`docId`，来自列表的 `rowkey`。

正文 `s5` 用作归档编号，列表 `rowkey` 作为访问别名。来源链接中的文书参数进行 URL 编码。

每次新生成随机 `pageId` 和 `ciphertext`。后者由 24 字符 salt、网站 IV 和对毫秒时间戳的 3DES-CBC/PKCS#7 加密构成，再转为空格分隔的二进制字符编码。网站 `strToBinary.js` 在 10–12 月分支使用零起算月份；这里为协议兼容保留该行为，包括短 IV 的零填充。这是网站协议的一部分。

响应的 `secretKey` 用作 3DES 密钥，IV 为北京时间 `yyyyMMdd`。跨午夜容许相邻日期尝试，解密后必须是合法 JSON。

`__RequestVerificationToken` 优先来自用户会话；缺失时按网站参数形态生成。HTTP 客户端使用 Cookie jar 接收更新 Cookie；重新导入会话后下一次请求使用新的会话。登录凭据由用户在浏览器中输入，会话保存后供 HTTP 客户端使用。

## 存储与恢复

`corpus.sqlite` 包含 `jobs,shards,documents,job_docs,shard_docs,paragraphs,events,requests,leases,codebooks,codings`。页面文书、任务归属和下一页在一个事务中提交；正文与段落在一个事务中提交。已保存正文通过本地语料库复用。采集锁使用 SQLite 租约协调同一数据目录的进程。

文书按稳定编号归档，原始列表字段和访问别名一同保留。方法附录明确区分文书数、可用正文数和未公开正文数。

CSV 为 UTF-8，标准双引号转义。Stata 118 的 numeric 用 double，短文本固定宽度，超过 2045 UTF-8 字节的文本用 strL。Stata 数值缺失和 JSON null 保持缺失语义；类目码与标签映射另存 codebook，避免自动重编号改变研究含义。
