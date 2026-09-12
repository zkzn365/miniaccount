# 独立实现声明（Clean Room Provenance）

## 结论

**本工程是独立实现。其中不含任何一行来自 Frappe Books 的代码、数据或资源。**

Frappe Books（<https://github.com/frappe/books>）以 **AGPL-3.0-only** 授权。
本工程的目标形态是**闭源商业产品**，因此与该项目之间必须保持严格的法律隔离。
本文件记录隔离措施，供尽职调查与代码审计使用。

---

## 1. 隔离措施

### 1.1 需求侧 vs 实现侧分离

| 阶段 | 允许 | 禁止 |
| --- | --- | --- |
| **需求分析** | 阅读 Frappe Books，理解其功能范围、交互流程、已知缺陷 | — |
| **设计** | 以其架构为**参考**，写出本工程自己的设计方案 | 复制其 schema / 表结构 / 字段命名 |
| **实现** | 只依据 `docs/` 下的设计文档与中国会计准则编写 | **对照其源码逐行翻译** |

### 1.2 明确的禁止项（已执行）

- ❌ 未复制、翻译、改写任何 `.ts` / `.vue` / `.js` 文件
- ❌ 未复制 `schemas/**/*.json`（本工程的 DDL 为自行设计，见 `docs/02-Go重写方案.md` §4）
- ❌ 未复制 `src/setup/standardCOA.ts` 或 `fixtures/verified/*.json`（科目表取自财政部原文）
- ❌ 未复制 `translations/*.csv`（术语自行整理，且其 zh-CN 术语不合大陆习惯）
- ❌ 未将 Frappe Books 作为依赖、子模块或构建输入
- ❌ 未使用其图标、字体、模板或截图

### 1.3 命名体系

Go 代码中的标识符**不沿用** Frappe Books 的命名。对照：

| Frappe Books | 本工程 | 说明 |
| --- | --- | --- |
| `AccountingLedgerEntry` | `ledger.Entry` | |
| `LedgerPosting` | `ledger.Posting` | |
| `JournalEntry` / `JournalEntryAccount` | `voucher`（计划中） | |
| `Account.rootType` | `account.RootType` | 且本工程为六类，对方五类 |
| `isCredit()` | `account.NaturalBalanceDir()` + `BalanceDir` 字段 | 本工程支持备抵科目 |
| `NumberSeries.current` | 期间内凭证字号（计划中） | 对方为全局计数器 |
| `party` | `contact` + `Aux` 四维 | |
| `LedgerReport` / `AccountReport` | 计划的 `report` 包 | |

> 表中右列是**本工程根据中国会计实务独立设计**的结果，
> 而非对左列的重命名 —— 例如 `BalanceDir` 字段、六类 `RootType`、
> 四维辅助核算，都是为了解决左列设计在中国不可用的问题而新增的。

---

## 2. 第三方依赖及其许可证

本工程须保持**允许闭源商用**的依赖集合。

| 依赖 | 用途 | 许可证 | 可商用 |
| --- | --- | --- | --- |
| 标准库 `math/bits` `encoding/csv` 等 | 基础能力 | BSD-3-Clause（Go 本身） | ✅ |
| `modernc.org/sqlite` | 纯 Go SQLite 驱动 | BSD-3-Clause | ✅ |
| `github.com/xuri/excelize/v2` | Excel 导出 | BSD-3-Clause | ✅ |
| `github.com/pressly/goose/v3` | 数据库迁移 | MIT | ✅ |
| Wails v2 | 桌面框架 | MIT | ✅ |
| Vue 3 / Vite / shadcn-vue | 前端 | MIT | ✅ |
| 思源宋体 / 思源黑体（字体） | PDF 中文 | SIL OFL 1.1 | ✅ |

### 2.1 必须阻断的许可证

CI 中应加入许可证扫描，**阻断**以下许可证进入依赖树：

- `AGPL-3.0` / `AGPL-3.0-only` / `AGPL-3.0-or-later`
- `GPL-2.0` / `GPL-3.0`（及其变体）
- `SSPL`、`BUSL`、`Commons Clause` 等非 OSI 或限制商用的许可

推荐工具：`go-licenses check ./...` 或 `syft` + `grype`。

```bash
# 建议加入 CI
go-licenses check ./... \
  --disallowed_types=forbidden,restricted,reciprocal
```

> ⚠️ 特别注意：**不要**因为「某个 Go 库用起来方便」而引入 GPL 系依赖。
> SQLite 的 C 绑定 `github.com/mattn/go-sqlite3` 是 MIT，安全；
> 但部分 PDF / 字体 / 图形库是 GPL，需逐个确认。

---

## 3. 本工程的数据来源

| 数据 | 来源 | 授权 |
| --- | --- | --- |
| 会计科目表（66 个一级科目） | 财政部《小企业会计准则》附录（财会〔2011〕17号） | 政府规范性文件，可自由使用 |
| 资产负债表 53 行 / 利润表 32 行及勾稽关系 | 同上 | 同上 |
| 明细科目设计 | 依据上述准则 + 营改增后实务口径（财会〔2016〕22号）自行整理 | — |
| 人民币大写规则 | 中国人民银行《正确填写票据和结算凭证的基本规定》 | 同上 |

原始依据文件与提取方法见 [`docs/reference/README.md`](../docs/reference/README.md)。

---

## 4. 变更控制

1. **任何新增依赖**须在 PR 中声明许可证，并通过 CI 扫描。
2. **任何来自外部项目的代码片段**必须在此文件中登记来源与许可证；
   无法确认授权的，不得合入。
3. 参与开发的人员若曾阅读 Frappe Books 源码，应在实现对应模块时
   **仅依据 `docs/` 下的设计文档**，不直接对照其源码。

---

## 5. 声明

本工程作者声明：本工程为独立创作，未使用 Frappe Books 或其他 AGPL/GPL
项目的源代码。本工程所借鉴的是**会计软件的通用设计范式**
（复式记账、扁平追加式总账、凭证生命周期等），
这些属于思想与方法，不受版权保护。

任何相反的证据如被发现，应在第一时间在此文件中记录并整改。

---

*最后更新：随本工程首次提交*
