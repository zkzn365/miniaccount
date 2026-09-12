export namespace audit {
	
	export class Entry {
	    seq: number;
	    at: string;
	    operator: string;
	    source: string;
	    action: string;
	    category: string;
	    summary: string;
	    entity: string;
	    entityId: string;
	    detail?: Record<string, any>;
	    result: string;
	    message?: string;
	    book: string;
	    company?: string;
	    appVersion?: string;
	    prevHash: string;
	    hash: string;
	
	    static createFrom(source: any = {}) {
	        return new Entry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.seq = source["seq"];
	        this.at = source["at"];
	        this.operator = source["operator"];
	        this.source = source["source"];
	        this.action = source["action"];
	        this.category = source["category"];
	        this.summary = source["summary"];
	        this.entity = source["entity"];
	        this.entityId = source["entityId"];
	        this.detail = source["detail"];
	        this.result = source["result"];
	        this.message = source["message"];
	        this.book = source["book"];
	        this.company = source["company"];
	        this.appVersion = source["appVersion"];
	        this.prevHash = source["prevHash"];
	        this.hash = source["hash"];
	    }
	}
	export class Page {
	    entries: Entry[];
	    total: number;
	    segments: number;
	    bytes: number;
	    truncated: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Page(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.entries = this.convertValues(source["entries"], Entry);
	        this.total = source["total"];
	        this.segments = source["segments"];
	        this.bytes = source["bytes"];
	        this.truncated = source["truncated"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class VerifyIssue {
	    seq: number;
	    file: string;
	    reason: string;
	
	    static createFrom(source: any = {}) {
	        return new VerifyIssue(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.seq = source["seq"];
	        this.file = source["file"];
	        this.reason = source["reason"];
	    }
	}
	export class VerifyResult {
	    ok: boolean;
	    checked: number;
	    files: number;
	    issues: VerifyIssue[];
	    head: string;
	
	    static createFrom(source: any = {}) {
	        return new VerifyResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.checked = source["checked"];
	        this.files = source["files"];
	        this.issues = this.convertValues(source["issues"], VerifyIssue);
	        this.head = source["head"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace calendar {
	
	export class Date {
	    year: number;
	    month: number;
	    day: number;
	
	    static createFrom(source: any = {}) {
	        return new Date(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.year = source["year"];
	        this.month = source["month"];
	        this.day = source["day"];
	    }
	}

}

export namespace main {
	
	export class AIAcceptRequest {
	    id: number;
	    createdBy: string;
	
	    static createFrom(source: any = {}) {
	        return new AIAcceptRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.createdBy = source["createdBy"];
	    }
	}
	export class AIAgentItemRequest {
	    runId: string;
	    index: number;
	    createdBy: string;
	    reason: string;
	
	    static createFrom(source: any = {}) {
	        return new AIAgentItemRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.runId = source["runId"];
	        this.index = source["index"];
	        this.createdBy = source["createdBy"];
	        this.reason = source["reason"];
	    }
	}
	export class AISuggestRequest {
	    task: string;
	    text: string;
	    amountYuan: string;
	    date: string;
	    counterparty: string;
	    direction: string;
	
	    static createFrom(source: any = {}) {
	        return new AISuggestRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.task = source["task"];
	        this.text = source["text"];
	        this.amountYuan = source["amountYuan"];
	        this.date = source["date"];
	        this.counterparty = source["counterparty"];
	        this.direction = source["direction"];
	    }
	}
	export class AccountKindOption {
	    value: string;
	    label: string;
	
	    static createFrom(source: any = {}) {
	        return new AccountKindOption(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.value = source["value"];
	        this.label = source["label"];
	    }
	}
	export class AccountKindsView {
	    rootTypes: AccountKindOption[];
	    auxTypes: AccountKindOption[];
	    maxLevel: number;
	
	    static createFrom(source: any = {}) {
	        return new AccountKindsView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rootTypes = this.convertValues(source["rootTypes"], AccountKindOption);
	        this.auxTypes = this.convertValues(source["auxTypes"], AccountKindOption);
	        this.maxLevel = source["maxLevel"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AdjustSalaryRequest {
	    id: number;
	    baseSalary: string;
	    siBase: string;
	    hfbBase: string;
	    reason: string;
	    operator: string;
	
	    static createFrom(source: any = {}) {
	        return new AdjustSalaryRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.baseSalary = source["baseSalary"];
	        this.siBase = source["siBase"];
	        this.hfbBase = source["hfbBase"];
	        this.reason = source["reason"];
	        this.operator = source["operator"];
	    }
	}
	export class AgingRequest {
	    asOf: string;
	    accountPrefix: string;
	
	    static createFrom(source: any = {}) {
	        return new AgingRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.asOf = source["asOf"];
	        this.accountPrefix = source["accountPrefix"];
	    }
	}
	export class AttachmentAudit {
	    posted: number;
	    withAttach: number;
	    without: number;
	    orphanFiles: number;
	
	    static createFrom(source: any = {}) {
	        return new AttachmentAudit(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.posted = source["posted"];
	        this.withAttach = source["withAttach"];
	        this.without = source["without"];
	        this.orphanFiles = source["orphanFiles"];
	    }
	}
	export class AttachmentQueryRequest {
	    ownerType: string;
	    ownerId: number;
	
	    static createFrom(source: any = {}) {
	        return new AttachmentQueryRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ownerType = source["ownerType"];
	        this.ownerId = source["ownerId"];
	    }
	}
	export class AuditLogPage {
	    page?: audit.Page;
	    dir: string;
	    actions: service.AuditActionOption[];
	    categories: string[];
	
	    static createFrom(source: any = {}) {
	        return new AuditLogPage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.page = this.convertValues(source["page"], audit.Page);
	        this.dir = source["dir"];
	        this.actions = this.convertValues(source["actions"], service.AuditActionOption);
	        this.categories = source["categories"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class BackupRequest {
	    dest: string;
	    includeFiles: boolean;
	
	    static createFrom(source: any = {}) {
	        return new BackupRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.dest = source["dest"];
	        this.includeFiles = source["includeFiles"];
	    }
	}
	export class BankFlowQueryRequest {
	    status: string;
	    from: string;
	    to: string;
	    limit: number;
	
	    static createFrom(source: any = {}) {
	        return new BankFlowQueryRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.from = source["from"];
	        this.to = source["to"];
	        this.limit = source["limit"];
	    }
	}
	export class BankImportRequest {
	    accountCode: string;
	    fileName: string;
	    dataBase64: string;
	    importedBy: string;
	
	    static createFrom(source: any = {}) {
	        return new BankImportRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.accountCode = source["accountCode"];
	        this.fileName = source["fileName"];
	        this.dataBase64 = source["dataBase64"];
	        this.importedBy = source["importedBy"];
	    }
	}
	export class BankPostRequest {
	    ids: number[];
	    postingBy: string;
	
	    static createFrom(source: any = {}) {
	        return new BankPostRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ids = source["ids"];
	        this.postingBy = source["postingBy"];
	    }
	}
	export class BankRuleRequest {
	    name: string;
	    pattern: string;
	    counterAccountCode: string;
	    direction: string;
	    contactId?: number;
	    employeeId?: number;
	    deptId?: number;
	    projectId?: number;
	
	    static createFrom(source: any = {}) {
	        return new BankRuleRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.pattern = source["pattern"];
	        this.counterAccountCode = source["counterAccountCode"];
	        this.direction = source["direction"];
	        this.contactId = source["contactId"];
	        this.employeeId = source["employeeId"];
	        this.deptId = source["deptId"];
	        this.projectId = source["projectId"];
	    }
	}
	export class BankSuggestionRequest {
	    flowId: number;
	    counterAccountCode: string;
	    contactId?: number;
	    employeeId?: number;
	    deptId?: number;
	    projectId?: number;
	    memo: string;
	
	    static createFrom(source: any = {}) {
	        return new BankSuggestionRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.flowId = source["flowId"];
	        this.counterAccountCode = source["counterAccountCode"];
	        this.contactId = source["contactId"];
	        this.employeeId = source["employeeId"];
	        this.deptId = source["deptId"];
	        this.projectId = source["projectId"];
	        this.memo = source["memo"];
	    }
	}
	export class BookList {
	    dir: string;
	    books: sqlite.BookPeek[];
	    others: sqlite.BookPeek[];
	    lastPath: string;
	    suggested: string;
	    hasAny: boolean;
	
	    static createFrom(source: any = {}) {
	        return new BookList(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.dir = source["dir"];
	        this.books = this.convertValues(source["books"], sqlite.BookPeek);
	        this.others = this.convertValues(source["others"], sqlite.BookPeek);
	        this.lastPath = source["lastPath"];
	        this.suggested = source["suggested"];
	        this.hasAny = source["hasAny"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class BookStatus {
	    open: boolean;
	    path: string;
	    book?: service.BookInfo;
	    recentPath?: string;
	
	    static createFrom(source: any = {}) {
	        return new BookStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.open = source["open"];
	        this.path = source["path"];
	        this.book = this.convertValues(source["book"], service.BookInfo);
	        this.recentPath = source["recentPath"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ClaimActionRequest {
	    id: number;
	    approverId: number;
	    postingBy: string;
	    reason: string;
	
	    static createFrom(source: any = {}) {
	        return new ClaimActionRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.approverId = source["approverId"];
	        this.postingBy = source["postingBy"];
	        this.reason = source["reason"];
	    }
	}
	export class ClaimItemRequest {
	    category: string;
	    occurDate: string;
	    summary: string;
	    amountYuan: string;
	    taxYuan: string;
	    accountCode: string;
	    deptId?: number;
	    invoiceId?: number;
	
	    static createFrom(source: any = {}) {
	        return new ClaimItemRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.category = source["category"];
	        this.occurDate = source["occurDate"];
	        this.summary = source["summary"];
	        this.amountYuan = source["amountYuan"];
	        this.taxYuan = source["taxYuan"];
	        this.accountCode = source["accountCode"];
	        this.deptId = source["deptId"];
	        this.invoiceId = source["invoiceId"];
	    }
	}
	export class ClaimQueryRequest {
	    status: string;
	    from: string;
	    to: string;
	
	    static createFrom(source: any = {}) {
	        return new ClaimQueryRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.from = source["from"];
	        this.to = source["to"];
	    }
	}
	export class ClaimRequest {
	    id: number;
	    claimantEmployeeId: number;
	    deptId?: number;
	    applyDate: string;
	    tripStart: string;
	    tripEnd: string;
	    destination: string;
	    reason: string;
	    payFromAccount: string;
	    remark: string;
	    items: ClaimItemRequest[];
	
	    static createFrom(source: any = {}) {
	        return new ClaimRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.claimantEmployeeId = source["claimantEmployeeId"];
	        this.deptId = source["deptId"];
	        this.applyDate = source["applyDate"];
	        this.tripStart = source["tripStart"];
	        this.tripEnd = source["tripEnd"];
	        this.destination = source["destination"];
	        this.reason = source["reason"];
	        this.payFromAccount = source["payFromAccount"];
	        this.remark = source["remark"];
	        this.items = this.convertValues(source["items"], ClaimItemRequest);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ColumnarRequest {
	    accountCode: string;
	    from: string;
	    to: string;
	
	    static createFrom(source: any = {}) {
	        return new ColumnarRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.accountCode = source["accountCode"];
	        this.from = source["from"];
	        this.to = source["to"];
	    }
	}
	export class ContactDetail {
	    id: number;
	    kind: string;
	    kindLabel: string;
	    name: string;
	    shortName: string;
	    taxNo: string;
	    bankName: string;
	    bankAccount: string;
	    contactPerson: string;
	    phone: string;
	    address: string;
	    enabled: boolean;
	    remark: string;
	
	    static createFrom(source: any = {}) {
	        return new ContactDetail(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.kindLabel = source["kindLabel"];
	        this.name = source["name"];
	        this.shortName = source["shortName"];
	        this.taxNo = source["taxNo"];
	        this.bankName = source["bankName"];
	        this.bankAccount = source["bankAccount"];
	        this.contactPerson = source["contactPerson"];
	        this.phone = source["phone"];
	        this.address = source["address"];
	        this.enabled = source["enabled"];
	        this.remark = source["remark"];
	    }
	}
	export class ContactRequest {
	    id: number;
	    kind: string;
	    name: string;
	    shortName: string;
	    taxNo: string;
	    bankName: string;
	    bankAccount: string;
	    contactPerson: string;
	    phone: string;
	    address: string;
	    enabled: boolean;
	    remark: string;
	
	    static createFrom(source: any = {}) {
	        return new ContactRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.name = source["name"];
	        this.shortName = source["shortName"];
	        this.taxNo = source["taxNo"];
	        this.bankName = source["bankName"];
	        this.bankAccount = source["bankAccount"];
	        this.contactPerson = source["contactPerson"];
	        this.phone = source["phone"];
	        this.address = source["address"];
	        this.enabled = source["enabled"];
	        this.remark = source["remark"];
	    }
	}
	export class DepartmentRequest {
	    id: number;
	    code: string;
	    name: string;
	    parentId?: number;
	    enabled: boolean;
	    remark: string;
	
	    static createFrom(source: any = {}) {
	        return new DepartmentRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.code = source["code"];
	        this.name = source["name"];
	        this.parentId = source["parentId"];
	        this.enabled = source["enabled"];
	        this.remark = source["remark"];
	    }
	}
	export class EmployeeRequest {
	    id: number;
	    code: string;
	    name: string;
	    idCard: string;
	    phone: string;
	    bankName: string;
	    bankAccount: string;
	    baseSalary: string;
	    siBase: string;
	    hfbBase: string;
	    siProfile: string;
	    specialAdditional: string;
	    deptId?: number;
	    position: string;
	    expenseAccountCode: string;
	    hireDate: string;
	    leaveDate: string;
	    enabled: boolean;
	    remark: string;
	
	    static createFrom(source: any = {}) {
	        return new EmployeeRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.code = source["code"];
	        this.name = source["name"];
	        this.idCard = source["idCard"];
	        this.phone = source["phone"];
	        this.bankName = source["bankName"];
	        this.bankAccount = source["bankAccount"];
	        this.baseSalary = source["baseSalary"];
	        this.siBase = source["siBase"];
	        this.hfbBase = source["hfbBase"];
	        this.siProfile = source["siProfile"];
	        this.specialAdditional = source["specialAdditional"];
	        this.deptId = source["deptId"];
	        this.position = source["position"];
	        this.expenseAccountCode = source["expenseAccountCode"];
	        this.hireDate = source["hireDate"];
	        this.leaveDate = source["leaveDate"];
	        this.enabled = source["enabled"];
	        this.remark = source["remark"];
	    }
	}
	export class ExportRequest {
	    kind: string;
	    dest: string;
	    year: number;
	    month: number;
	    accountPrefix: string;
	    runId: number;
	
	    static createFrom(source: any = {}) {
	        return new ExportRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.dest = source["dest"];
	        this.year = source["year"];
	        this.month = source["month"];
	        this.accountPrefix = source["accountPrefix"];
	        this.runId = source["runId"];
	    }
	}
	export class InvoiceQueryRequest {
	    direction: string;
	    status: string;
	    from: string;
	    to: string;
	
	    static createFrom(source: any = {}) {
	        return new InvoiceQueryRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.direction = source["direction"];
	        this.status = source["status"];
	        this.from = source["from"];
	        this.to = source["to"];
	    }
	}
	export class InvoiceRequest {
	    id: number;
	    direction: string;
	    kind: string;
	    code: string;
	    number: string;
	    invoiceDate: string;
	    sellerName: string;
	    sellerTaxNo: string;
	    buyerName: string;
	    buyerTaxNo: string;
	    amountExTax: string;
	    taxRatePpm: number;
	    taxAmount: string;
	    totalAmount: string;
	    category: string;
	    contactId?: number;
	    remark: string;
	    expenseAccount: string;
	    deptId?: number;
	
	    static createFrom(source: any = {}) {
	        return new InvoiceRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.direction = source["direction"];
	        this.kind = source["kind"];
	        this.code = source["code"];
	        this.number = source["number"];
	        this.invoiceDate = source["invoiceDate"];
	        this.sellerName = source["sellerName"];
	        this.sellerTaxNo = source["sellerTaxNo"];
	        this.buyerName = source["buyerName"];
	        this.buyerTaxNo = source["buyerTaxNo"];
	        this.amountExTax = source["amountExTax"];
	        this.taxRatePpm = source["taxRatePpm"];
	        this.taxAmount = source["taxAmount"];
	        this.totalAmount = source["totalAmount"];
	        this.category = source["category"];
	        this.contactId = source["contactId"];
	        this.remark = source["remark"];
	        this.expenseAccount = source["expenseAccount"];
	        this.deptId = source["deptId"];
	    }
	}
	export class LedgerRequest {
	    accountPrefix: string;
	    from: string;
	    to: string;
	    limit: number;
	
	    static createFrom(source: any = {}) {
	        return new LedgerRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.accountPrefix = source["accountPrefix"];
	        this.from = source["from"];
	        this.to = source["to"];
	        this.limit = source["limit"];
	    }
	}
	export class LedgerRow {
	    date: string;
	    voucher: string;
	    summary: string;
	    debit: number;
	    credit: number;
	    balance: number;
	    dir: string;
	    contra: string;
	
	    static createFrom(source: any = {}) {
	        return new LedgerRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.date = source["date"];
	        this.voucher = source["voucher"];
	        this.summary = source["summary"];
	        this.debit = source["debit"];
	        this.credit = source["credit"];
	        this.balance = source["balance"];
	        this.dir = source["dir"];
	        this.contra = source["contra"];
	    }
	}
	export class LedgerResult {
	    accountPrefix: string;
	    accountName: string;
	    from: string;
	    to: string;
	    rows: LedgerRow[];
	    openingBalance: number;
	    closingBalance: number;
	    truncated: boolean;
	
	    static createFrom(source: any = {}) {
	        return new LedgerResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.accountPrefix = source["accountPrefix"];
	        this.accountName = source["accountName"];
	        this.from = source["from"];
	        this.to = source["to"];
	        this.rows = this.convertValues(source["rows"], LedgerRow);
	        this.openingBalance = source["openingBalance"];
	        this.closingBalance = source["closingBalance"];
	        this.truncated = source["truncated"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class PayrollRunRequest {
	    year: number;
	    month: number;
	    createdBy: string;
	    onlyPostedHistory: boolean;
	    save: boolean;
	
	    static createFrom(source: any = {}) {
	        return new PayrollRunRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.year = source["year"];
	        this.month = source["month"];
	        this.createdBy = source["createdBy"];
	        this.onlyPostedHistory = source["onlyPostedHistory"];
	        this.save = source["save"];
	    }
	}
	export class PeriodRequest {
	    year: number;
	    month: number;
	    by?: string;
	
	    static createFrom(source: any = {}) {
	        return new PeriodRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.year = source["year"];
	        this.month = source["month"];
	        this.by = source["by"];
	    }
	}
	export class PostInvoiceRequest {
	    id: number;
	    postingBy: string;
	    expenseAccount: string;
	    deptId?: number;
	
	    static createFrom(source: any = {}) {
	        return new PostInvoiceRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.postingBy = source["postingBy"];
	        this.expenseAccount = source["expenseAccount"];
	        this.deptId = source["deptId"];
	    }
	}
	export class ReconciliationRequest {
	    accountCode: string;
	    asOf: string;
	    from: string;
	    bankBalance?: number;
	
	    static createFrom(source: any = {}) {
	        return new ReconciliationRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.accountCode = source["accountCode"];
	        this.asOf = source["asOf"];
	        this.from = source["from"];
	        this.bankBalance = source["bankBalance"];
	    }
	}
	export class RemoveAttachmentRequest {
	    ownerType: string;
	    ownerId: number;
	    hash: string;
	
	    static createFrom(source: any = {}) {
	        return new RemoveAttachmentRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ownerType = source["ownerType"];
	        this.ownerId = source["ownerId"];
	        this.hash = source["hash"];
	    }
	}
	export class ReportIssue {
	    text: string;
	    fatal: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ReportIssue(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.text = source["text"];
	        this.fatal = source["fatal"];
	    }
	}
	export class ReportRequest {
	    kind: string;
	    year: number;
	    month: number;
	    accountPrefix?: string;
	
	    static createFrom(source: any = {}) {
	        return new ReportRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.year = source["year"];
	        this.month = source["month"];
	        this.accountPrefix = source["accountPrefix"];
	    }
	}
	export class ReportRow {
	    no?: string;
	    label: string;
	    rightNo?: string;
	    rightLabel?: string;
	    indent: number;
	    bold: boolean;
	    values: number[];
	    isMemo?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ReportRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.no = source["no"];
	        this.label = source["label"];
	        this.rightNo = source["rightNo"];
	        this.rightLabel = source["rightLabel"];
	        this.indent = source["indent"];
	        this.bold = source["bold"];
	        this.values = source["values"];
	        this.isMemo = source["isMemo"];
	    }
	}
	export class ReportResult {
	    title: string;
	    subtitle: string;
	    columns: string[];
	    rows: ReportRow[];
	    issues?: ReportIssue[];
	
	    static createFrom(source: any = {}) {
	        return new ReportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.title = source["title"];
	        this.subtitle = source["subtitle"];
	        this.columns = source["columns"];
	        this.rows = this.convertValues(source["rows"], ReportRow);
	        this.issues = this.convertValues(source["issues"], ReportIssue);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class ResignEmployeeRequest {
	    id: number;
	    leaveDate: string;
	    reason: string;
	    operator: string;
	
	    static createFrom(source: any = {}) {
	        return new ResignEmployeeRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.leaveDate = source["leaveDate"];
	        this.reason = source["reason"];
	        this.operator = source["operator"];
	    }
	}
	export class RestoreRequest {
	    archive: string;
	    dbPath: string;
	
	    static createFrom(source: any = {}) {
	        return new RestoreRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.archive = source["archive"];
	        this.dbPath = source["dbPath"];
	    }
	}
	export class RestoreResult {
	    dbPath: string;
	    filesDir: string;
	    restored: number;
	    stashed: string;
	    companyName: string;
	
	    static createFrom(source: any = {}) {
	        return new RestoreResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.dbPath = source["dbPath"];
	        this.filesDir = source["filesDir"];
	        this.restored = source["restored"];
	        this.stashed = source["stashed"];
	        this.companyName = source["companyName"];
	    }
	}
	export class ReverseVoucherRequest {
	    id: number;
	    by: string;
	    date?: string;
	
	    static createFrom(source: any = {}) {
	        return new ReverseVoucherRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.by = source["by"];
	        this.date = source["date"];
	    }
	}
	export class ServiceSourceOption {
	    value: string;
	    label: string;
	
	    static createFrom(source: any = {}) {
	        return new ServiceSourceOption(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.value = source["value"];
	        this.label = source["label"];
	    }
	}
	export class SetVATStatusRequest {
	    status: string;
	    from: string;
	    note: string;
	
	    static createFrom(source: any = {}) {
	        return new SetVATStatusRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.from = source["from"];
	        this.note = source["note"];
	    }
	}
	export class StatementRequest {
	    contactId: number;
	    from: string;
	    to: string;
	    accountPrefix: string;
	
	    static createFrom(source: any = {}) {
	        return new StatementRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.contactId = source["contactId"];
	        this.from = source["from"];
	        this.to = source["to"];
	        this.accountPrefix = source["accountPrefix"];
	    }
	}
	export class SummaryRequest {
	    from: string;
	    to: string;
	
	    static createFrom(source: any = {}) {
	        return new SummaryRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.from = source["from"];
	        this.to = source["to"];
	    }
	}
	export class TransferEmployeeRequest {
	    id: number;
	    deptId: number;
	    reason: string;
	    operator: string;
	
	    static createFrom(source: any = {}) {
	        return new TransferEmployeeRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.deptId = source["deptId"];
	        this.reason = source["reason"];
	        this.operator = source["operator"];
	    }
	}
	export class UploadAttachmentRequest {
	    voucherId: number;
	    fileName: string;
	    dataBase64: string;
	
	    static createFrom(source: any = {}) {
	        return new UploadAttachmentRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.voucherId = source["voucherId"];
	        this.fileName = source["fileName"];
	        this.dataBase64 = source["dataBase64"];
	    }
	}
	export class VATRateQuery {
	    status: string;
	    subject: string;
	    category: string;
	    method: string;
	    on: string;
	    includeConditional: boolean;
	
	    static createFrom(source: any = {}) {
	        return new VATRateQuery(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.subject = source["subject"];
	        this.category = source["category"];
	        this.method = source["method"];
	        this.on = source["on"];
	        this.includeConditional = source["includeConditional"];
	    }
	}
	export class VoucherCheckResult {
	    ok: boolean;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new VoucherCheckResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.message = source["message"];
	    }
	}
	export class VoucherLineRequest {
	    accountCode: string;
	    summary: string;
	    debitYuan: string;
	    creditYuan: string;
	    contactId?: number;
	    employeeId?: number;
	    deptId?: number;
	    projectId?: number;
	
	    static createFrom(source: any = {}) {
	        return new VoucherLineRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.accountCode = source["accountCode"];
	        this.summary = source["summary"];
	        this.debitYuan = source["debitYuan"];
	        this.creditYuan = source["creditYuan"];
	        this.contactId = source["contactId"];
	        this.employeeId = source["employeeId"];
	        this.deptId = source["deptId"];
	        this.projectId = source["projectId"];
	    }
	}
	export class VoucherMeta {
	    accounts: service.AccountOption[];
	    contacts: service.ContactOption[];
	    words: string[];
	    currentPeriod: string;
	    today: string;
	
	    static createFrom(source: any = {}) {
	        return new VoucherMeta(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.accounts = this.convertValues(source["accounts"], service.AccountOption);
	        this.contacts = this.convertValues(source["contacts"], service.ContactOption);
	        this.words = source["words"];
	        this.currentPeriod = source["currentPeriod"];
	        this.today = source["today"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class VoucherQueryRequest {
	    year: number;
	    month: number;
	    status?: string;
	    keyword?: string;
	    limit?: number;
	
	    static createFrom(source: any = {}) {
	        return new VoucherQueryRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.year = source["year"];
	        this.month = source["month"];
	        this.status = source["status"];
	        this.keyword = source["keyword"];
	        this.limit = source["limit"];
	    }
	}
	export class VoucherRequest {
	    id: number;
	    word: string;
	    date: string;
	    remark: string;
	    attachCount: number;
	    lines: VoucherLineRequest[];
	    createdBy: string;
	    postedBy: string;
	
	    static createFrom(source: any = {}) {
	        return new VoucherRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.word = source["word"];
	        this.date = source["date"];
	        this.remark = source["remark"];
	        this.attachCount = source["attachCount"];
	        this.lines = this.convertValues(source["lines"], VoucherLineRequest);
	        this.createdBy = source["createdBy"];
	        this.postedBy = source["postedBy"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class backupManifestView {
	    path: string;
	    formatVersion: number;
	    appVersion: string;
	    companyName: string;
	    createdAt: string;
	    dbSize: number;
	    dbSha256: string;
	    fileCount: number;
	    fileBytes: number;
	    periodFrom: string;
	    periodTo: string;
	    voucherCount: number;
	    accountCount: number;
	
	    static createFrom(source: any = {}) {
	        return new backupManifestView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.formatVersion = source["formatVersion"];
	        this.appVersion = source["appVersion"];
	        this.companyName = source["companyName"];
	        this.createdAt = source["createdAt"];
	        this.dbSize = source["dbSize"];
	        this.dbSha256 = source["dbSha256"];
	        this.fileCount = source["fileCount"];
	        this.fileBytes = source["fileBytes"];
	        this.periodFrom = source["periodFrom"];
	        this.periodTo = source["periodTo"];
	        this.voucherCount = source["voucherCount"];
	        this.accountCount = source["accountCount"];
	    }
	}

}

export namespace payroll {
	
	export class InsuranceRates {
	    pensionSelf: number;
	    medicalSelf: number;
	    unemploymentSelf: number;
	    housingFundSelf: number;
	    pensionCo: number;
	    medicalCo: number;
	    unemploymentCo: number;
	    injuryCo: number;
	    maternityCo: number;
	    housingFundCo: number;
	
	    static createFrom(source: any = {}) {
	        return new InsuranceRates(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pensionSelf = source["pensionSelf"];
	        this.medicalSelf = source["medicalSelf"];
	        this.unemploymentSelf = source["unemploymentSelf"];
	        this.housingFundSelf = source["housingFundSelf"];
	        this.pensionCo = source["pensionCo"];
	        this.medicalCo = source["medicalCo"];
	        this.unemploymentCo = source["unemploymentCo"];
	        this.injuryCo = source["injuryCo"];
	        this.maternityCo = source["maternityCo"];
	        this.housingFundCo = source["housingFundCo"];
	    }
	}
	export class InsuranceScheme {
	    name: string;
	    city: string;
	    baseMin: number;
	    baseMax: number;
	    housingFundBaseMin: number;
	    housingFundBaseMax: number;
	    rates: InsuranceRates;
	    effectiveFrom: calendar.Date;
	
	    static createFrom(source: any = {}) {
	        return new InsuranceScheme(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.city = source["city"];
	        this.baseMin = source["baseMin"];
	        this.baseMax = source["baseMax"];
	        this.housingFundBaseMin = source["housingFundBaseMin"];
	        this.housingFundBaseMax = source["housingFundBaseMax"];
	        this.rates = this.convertValues(source["rates"], InsuranceRates);
	        this.effectiveFrom = this.convertValues(source["effectiveFrom"], calendar.Date);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace service {
	
	export class AIAcceptResult {
	    voucherId: number;
	    voucherNo: string;
	    summary: string;
	
	    static createFrom(source: any = {}) {
	        return new AIAcceptResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.voucherId = source["voucherId"];
	        this.voucherNo = source["voucherNo"];
	        this.summary = source["summary"];
	    }
	}
	export class ClosingEntry {
	    accountCode: string;
	    summary: string;
	    debit: number;
	    credit: number;
	    auxDesc: string;
	
	    static createFrom(source: any = {}) {
	        return new ClosingEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.accountCode = source["accountCode"];
	        this.summary = source["summary"];
	        this.debit = source["debit"];
	        this.credit = source["credit"];
	        this.auxDesc = source["auxDesc"];
	    }
	}
	export class VoucherDraft {
	    word: string;
	    bizDate: string;
	    remark: string;
	    entries: ClosingEntry[];
	    total: number;
	
	    static createFrom(source: any = {}) {
	        return new VoucherDraft(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.word = source["word"];
	        this.bizDate = source["bizDate"];
	        this.remark = source["remark"];
	        this.entries = this.convertValues(source["entries"], ClosingEntry);
	        this.total = source["total"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AIAgentItem {
	    targetType: string;
	    targetId: number;
	    label: string;
	    ok: boolean;
	    suggestionId: number;
	    summary: string;
	    error?: string;
	    confidence: number;
	    failures?: string[];
	    warnings?: string[];
	    voucher?: VoucherDraft;
	    decision?: string;
	
	    static createFrom(source: any = {}) {
	        return new AIAgentItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.targetType = source["targetType"];
	        this.targetId = source["targetId"];
	        this.label = source["label"];
	        this.ok = source["ok"];
	        this.suggestionId = source["suggestionId"];
	        this.summary = source["summary"];
	        this.error = source["error"];
	        this.confidence = source["confidence"];
	        this.failures = source["failures"];
	        this.warnings = source["warnings"];
	        this.voucher = this.convertValues(source["voucher"], VoucherDraft);
	        this.decision = source["decision"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AIAgentRequest {
	    Source: string;
	    Limit: number;
	    Date: string;
	
	    static createFrom(source: any = {}) {
	        return new AIAgentRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Source = source["Source"];
	        this.Limit = source["Limit"];
	        this.Date = source["Date"];
	    }
	}
	export class AIAgentRun {
	    id: string;
	    source: string;
	    state: string;
	    total: number;
	    done: number;
	    okCount: number;
	    items: AIAgentItem[];
	    model: string;
	    error?: string;
	    startedAt: string;
	    finishedAt?: string;
	    tokensIn: number;
	    tokensOut: number;
	
	    static createFrom(source: any = {}) {
	        return new AIAgentRun(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.source = source["source"];
	        this.state = source["state"];
	        this.total = source["total"];
	        this.done = source["done"];
	        this.okCount = source["okCount"];
	        this.items = this.convertValues(source["items"], AIAgentItem);
	        this.model = source["model"];
	        this.error = source["error"];
	        this.startedAt = source["startedAt"];
	        this.finishedAt = source["finishedAt"];
	        this.tokensIn = source["tokensIn"];
	        this.tokensOut = source["tokensOut"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AIConfig {
	    providers: sqlite.AIProviderConfig[];
	    stats: sqlite.AIStats;
	    hasDefault: boolean;
	
	    static createFrom(source: any = {}) {
	        return new AIConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.providers = this.convertValues(source["providers"], sqlite.AIProviderConfig);
	        this.stats = this.convertValues(source["stats"], sqlite.AIStats);
	        this.hasDefault = source["hasDefault"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AIPromptInput {
	    instructions: string;
	    taskNotes: Record<string, string>;
	
	    static createFrom(source: any = {}) {
	        return new AIPromptInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.instructions = source["instructions"];
	        this.taskNotes = source["taskNotes"];
	    }
	}
	export class AIPromptPreview {
	    system: string;
	    user: string;
	    digest: string;
	    chars: number;
	    task: string;
	
	    static createFrom(source: any = {}) {
	        return new AIPromptPreview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.system = source["system"];
	        this.user = source["user"];
	        this.digest = source["digest"];
	        this.chars = source["chars"];
	        this.task = source["task"];
	    }
	}
	export class AIPromptTask {
	    value: string;
	    label: string;
	    note: string;
	
	    static createFrom(source: any = {}) {
	        return new AIPromptTask(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.value = source["value"];
	        this.label = source["label"];
	        this.note = source["note"];
	    }
	}
	export class AIPromptView {
	    instructions: string;
	    custom: boolean;
	    default: string;
	    taskNotes: Record<string, string>;
	    tasks: AIPromptTask[];
	    updatedAt: string;
	    maxInstructions: number;
	    maxTaskNote: number;
	    locked: string[];
	
	    static createFrom(source: any = {}) {
	        return new AIPromptView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.instructions = source["instructions"];
	        this.custom = source["custom"];
	        this.default = source["default"];
	        this.taskNotes = source["taskNotes"];
	        this.tasks = this.convertValues(source["tasks"], AIPromptTask);
	        this.updatedAt = source["updatedAt"];
	        this.maxInstructions = source["maxInstructions"];
	        this.maxTaskNote = source["maxTaskNote"];
	        this.locked = source["locked"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class HealthItemInfo {
	    key: string;
	    title: string;
	    level: string;
	    detail: string;
	    count: number;
	
	    static createFrom(source: any = {}) {
	        return new HealthItemInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.title = source["title"];
	        this.level = source["level"];
	        this.detail = source["detail"];
	        this.count = source["count"];
	    }
	}
	export class AISuggestResult {
	    ok: boolean;
	    summary: string;
	    error: string;
	    suggestionId: number;
	    layer: string;
	    confidence: number;
	    voucher?: VoucherDraft;
	    failures: HealthItemInfo[];
	    warnings: HealthItemInfo[];
	    model: string;
	    latencyMs: number;
	    tokensIn: number;
	    tokensOut: number;
	
	    static createFrom(source: any = {}) {
	        return new AISuggestResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.summary = source["summary"];
	        this.error = source["error"];
	        this.suggestionId = source["suggestionId"];
	        this.layer = source["layer"];
	        this.confidence = source["confidence"];
	        this.voucher = this.convertValues(source["voucher"], VoucherDraft);
	        this.failures = this.convertValues(source["failures"], HealthItemInfo);
	        this.warnings = this.convertValues(source["warnings"], HealthItemInfo);
	        this.model = source["model"];
	        this.latencyMs = source["latencyMs"];
	        this.tokensIn = source["tokensIn"];
	        this.tokensOut = source["tokensOut"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AccountInput {
	    code: string;
	    name: string;
	    parentCode: string;
	    rootType: string;
	    balanceDir: string;
	    auxTypes: string[];
	    remark: string;
	    isLeaf?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new AccountInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.name = source["name"];
	        this.parentCode = source["parentCode"];
	        this.rootType = source["rootType"];
	        this.balanceDir = source["balanceDir"];
	        this.auxTypes = source["auxTypes"];
	        this.remark = source["remark"];
	        this.isLeaf = source["isLeaf"];
	    }
	}
	export class AccountOption {
	    code: string;
	    name: string;
	    fullName: string;
	    direction: string;
	    auxTypes: string[];
	    searchText: string;
	
	    static createFrom(source: any = {}) {
	        return new AccountOption(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.name = source["name"];
	        this.fullName = source["fullName"];
	        this.direction = source["direction"];
	        this.auxTypes = source["auxTypes"];
	        this.searchText = source["searchText"];
	    }
	}
	export class AccountOptionItem {
	    value: string;
	    label: string;
	
	    static createFrom(source: any = {}) {
	        return new AccountOptionItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.value = source["value"];
	        this.label = source["label"];
	    }
	}
	export class AccountRow {
	    id: number;
	    code: string;
	    name: string;
	    fullName: string;
	    parentCode: string;
	    level: number;
	    isLeaf: boolean;
	    rootType: string;
	    rootLabel: string;
	    balanceDir: string;
	    dirLabel: string;
	    auxTypes: string[];
	    auxLabels: string[];
	    isEnabled: boolean;
	    isPreset: boolean;
	    remark: string;
	    entryCount: number;
	    balance: number;
	    childCount: number;
	    canDelete: boolean;
	    reason?: string;
	
	    static createFrom(source: any = {}) {
	        return new AccountRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.code = source["code"];
	        this.name = source["name"];
	        this.fullName = source["fullName"];
	        this.parentCode = source["parentCode"];
	        this.level = source["level"];
	        this.isLeaf = source["isLeaf"];
	        this.rootType = source["rootType"];
	        this.rootLabel = source["rootLabel"];
	        this.balanceDir = source["balanceDir"];
	        this.dirLabel = source["dirLabel"];
	        this.auxTypes = source["auxTypes"];
	        this.auxLabels = source["auxLabels"];
	        this.isEnabled = source["isEnabled"];
	        this.isPreset = source["isPreset"];
	        this.remark = source["remark"];
	        this.entryCount = source["entryCount"];
	        this.balance = source["balance"];
	        this.childCount = source["childCount"];
	        this.canDelete = source["canDelete"];
	        this.reason = source["reason"];
	    }
	}
	export class AccountsView {
	    rows: AccountRow[];
	    total: number;
	    leafCount: number;
	    rootTypes: AccountOptionItem[];
	    auxTypes: AccountOptionItem[];
	    maxLevel: number;
	
	    static createFrom(source: any = {}) {
	        return new AccountsView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rows = this.convertValues(source["rows"], AccountRow);
	        this.total = source["total"];
	        this.leafCount = source["leafCount"];
	        this.rootTypes = this.convertValues(source["rootTypes"], AccountOptionItem);
	        this.auxTypes = this.convertValues(source["auxTypes"], AccountOptionItem);
	        this.maxLevel = source["maxLevel"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ActiveContactsView {
	    id: number;
	    name: string;
	    kind: string;
	    kindLabel: string;
	
	    static createFrom(source: any = {}) {
	        return new ActiveContactsView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.kind = source["kind"];
	        this.kindLabel = source["kindLabel"];
	    }
	}
	export class AgingBucketView {
	    label: string;
	    amount: number;
	    percent: number;
	
	    static createFrom(source: any = {}) {
	        return new AgingBucketView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.label = source["label"];
	        this.amount = source["amount"];
	        this.percent = source["percent"];
	    }
	}
	export class AgingItemView {
	    date: string;
	    days: number;
	    amount: number;
	    summary: string;
	    voucherNo: string;
	    bucketLabel: string;
	
	    static createFrom(source: any = {}) {
	        return new AgingItemView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.date = source["date"];
	        this.days = source["days"];
	        this.amount = source["amount"];
	        this.summary = source["summary"];
	        this.voucherNo = source["voucherNo"];
	        this.bucketLabel = source["bucketLabel"];
	    }
	}
	export class AgingRowView {
	    contactId: number;
	    contactName: string;
	    accountCode: string;
	    accountName: string;
	    debits: number;
	    credits: number;
	    balance: number;
	    buckets: AgingBucketView[];
	    items: AgingItemView[];
	    maxDays: number;
	
	    static createFrom(source: any = {}) {
	        return new AgingRowView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.contactId = source["contactId"];
	        this.contactName = source["contactName"];
	        this.accountCode = source["accountCode"];
	        this.accountName = source["accountName"];
	        this.debits = source["debits"];
	        this.credits = source["credits"];
	        this.balance = source["balance"];
	        this.buckets = this.convertValues(source["buckets"], AgingBucketView);
	        this.items = this.convertValues(source["items"], AgingItemView);
	        this.maxDays = source["maxDays"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AgingView {
	    asOf: string;
	    buckets: AgingBucketView[];
	    rows: AgingRowView[];
	    total: number;
	    debitTotal: number;
	    creditTotal: number;
	    summary: string;
	    over90: number;
	
	    static createFrom(source: any = {}) {
	        return new AgingView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.asOf = source["asOf"];
	        this.buckets = this.convertValues(source["buckets"], AgingBucketView);
	        this.rows = this.convertValues(source["rows"], AgingRowView);
	        this.total = source["total"];
	        this.debitTotal = source["debitTotal"];
	        this.creditTotal = source["creditTotal"];
	        this.summary = source["summary"];
	        this.over90 = source["over90"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AttachmentInfo {
	    hash: string;
	    name: string;
	    size: number;
	    mime: string;
	    path: string;
	    missing: boolean;
	
	    static createFrom(source: any = {}) {
	        return new AttachmentInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hash = source["hash"];
	        this.name = source["name"];
	        this.size = source["size"];
	        this.mime = source["mime"];
	        this.path = source["path"];
	        this.missing = source["missing"];
	    }
	}
	export class AuditActionOption {
	    value: string;
	    label: string;
	
	    static createFrom(source: any = {}) {
	        return new AuditActionOption(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.value = source["value"];
	        this.label = source["label"];
	    }
	}
	export class AuditQuery {
	    operator: string;
	    from: string;
	    to: string;
	    actions: string[];
	    result: string;
	    text: string;
	    limit: number;
	    offset: number;
	    categories: string[];
	    bookOnly: boolean;
	
	    static createFrom(source: any = {}) {
	        return new AuditQuery(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.operator = source["operator"];
	        this.from = source["from"];
	        this.to = source["to"];
	        this.actions = source["actions"];
	        this.result = source["result"];
	        this.text = source["text"];
	        this.limit = source["limit"];
	        this.offset = source["offset"];
	        this.categories = source["categories"];
	        this.bookOnly = source["bookOnly"];
	    }
	}
	export class BankFlowView {
	    id: number;
	    date: string;
	    amount: number;
	    direction: string;
	    directionLabel: string;
	    counterpartyName: string;
	    summary: string;
	    serialNo: string;
	    balance: number;
	    status: string;
	    statusLabel: string;
	    counterAccount: string;
	    matchLayer: string;
	    matchLayerLabel: string;
	    confidence: number;
	    memo: string;
	    voucherNo: string;
	
	    static createFrom(source: any = {}) {
	        return new BankFlowView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.date = source["date"];
	        this.amount = source["amount"];
	        this.direction = source["direction"];
	        this.directionLabel = source["directionLabel"];
	        this.counterpartyName = source["counterpartyName"];
	        this.summary = source["summary"];
	        this.serialNo = source["serialNo"];
	        this.balance = source["balance"];
	        this.status = source["status"];
	        this.statusLabel = source["statusLabel"];
	        this.counterAccount = source["counterAccount"];
	        this.matchLayer = source["matchLayer"];
	        this.matchLayerLabel = source["matchLayerLabel"];
	        this.confidence = source["confidence"];
	        this.memo = source["memo"];
	        this.voucherNo = source["voucherNo"];
	    }
	}
	export class BankImportResult {
	    importId: number;
	    total: number;
	    inserted: number;
	    duplicated: number;
	    totalIn: number;
	    totalOut: number;
	    from: string;
	    to: string;
	    encoding: string;
	    mapping: string;
	    parseErrors: string[];
	
	    static createFrom(source: any = {}) {
	        return new BankImportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.importId = source["importId"];
	        this.total = source["total"];
	        this.inserted = source["inserted"];
	        this.duplicated = source["duplicated"];
	        this.totalIn = source["totalIn"];
	        this.totalOut = source["totalOut"];
	        this.from = source["from"];
	        this.to = source["to"];
	        this.encoding = source["encoding"];
	        this.mapping = source["mapping"];
	        this.parseErrors = source["parseErrors"];
	    }
	}
	export class BankMatchResult {
	    total: number;
	    matched: number;
	    byLayer: Record<string, number>;
	    unmatched: number[];
	
	    static createFrom(source: any = {}) {
	        return new BankMatchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.total = source["total"];
	        this.matched = source["matched"];
	        this.byLayer = source["byLayer"];
	        this.unmatched = source["unmatched"];
	    }
	}
	export class BankPostResult {
	    created: number;
	    skipped: number;
	    failures: string[];
	
	    static createFrom(source: any = {}) {
	        return new BankPostResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.created = source["created"];
	        this.skipped = source["skipped"];
	        this.failures = source["failures"];
	    }
	}
	export class BankRuleView {
	    id: number;
	    name: string;
	    pattern: string;
	    counterAccountCode: string;
	    direction: string;
	    contactId?: number;
	    employeeId?: number;
	    deptId?: number;
	    projectId?: number;
	    hitCount: number;
	    enabled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new BankRuleView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.pattern = source["pattern"];
	        this.counterAccountCode = source["counterAccountCode"];
	        this.direction = source["direction"];
	        this.contactId = source["contactId"];
	        this.employeeId = source["employeeId"];
	        this.deptId = source["deptId"];
	        this.projectId = source["projectId"];
	        this.hitCount = source["hitCount"];
	        this.enabled = source["enabled"];
	    }
	}
	export class BankStats {
	    imported: number;
	    matched: number;
	    posted: number;
	    ignored: number;
	
	    static createFrom(source: any = {}) {
	        return new BankStats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.imported = source["imported"];
	        this.matched = source["matched"];
	        this.posted = source["posted"];
	        this.ignored = source["ignored"];
	    }
	}
	export class HealthInfo {
	    period: string;
	    canClose: boolean;
	    summary: string;
	    items: HealthItemInfo[];
	    errors: HealthItemInfo[];
	    warnings: HealthItemInfo[];
	
	    static createFrom(source: any = {}) {
	        return new HealthInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.period = source["period"];
	        this.canClose = source["canClose"];
	        this.summary = source["summary"];
	        this.items = this.convertValues(source["items"], HealthItemInfo);
	        this.errors = this.convertValues(source["errors"], HealthItemInfo);
	        this.warnings = this.convertValues(source["warnings"], HealthItemInfo);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PeriodInfo {
	    year: number;
	    month: number;
	    label: string;
	    status: string;
	    statusLabel: string;
	    from: string;
	    to: string;
	    voucherCount: number;
	    canClose: boolean;
	    canReopen: boolean;
	    health?: HealthInfo;
	
	    static createFrom(source: any = {}) {
	        return new PeriodInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.year = source["year"];
	        this.month = source["month"];
	        this.label = source["label"];
	        this.status = source["status"];
	        this.statusLabel = source["statusLabel"];
	        this.from = source["from"];
	        this.to = source["to"];
	        this.voucherCount = source["voucherCount"];
	        this.canClose = source["canClose"];
	        this.canReopen = source["canReopen"];
	        this.health = this.convertValues(source["health"], HealthInfo);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class BookInfo {
	    companyName: string;
	    creditCode: string;
	    legalPerson: string;
	    taxType: string;
	    taxTypeLabel: string;
	    enterpriseScale: string;
	    enterpriseScaleLabel: string;
	    vatStatus: string;
	    vatStatusLabel: string;
	    vatStatusEffectiveFrom: string;
	    canDeductInputVat: boolean;
	    startPeriod: string;
	    periods: PeriodInfo[];
	
	    static createFrom(source: any = {}) {
	        return new BookInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.companyName = source["companyName"];
	        this.creditCode = source["creditCode"];
	        this.legalPerson = source["legalPerson"];
	        this.taxType = source["taxType"];
	        this.taxTypeLabel = source["taxTypeLabel"];
	        this.enterpriseScale = source["enterpriseScale"];
	        this.enterpriseScaleLabel = source["enterpriseScaleLabel"];
	        this.vatStatus = source["vatStatus"];
	        this.vatStatusLabel = source["vatStatusLabel"];
	        this.vatStatusEffectiveFrom = source["vatStatusEffectiveFrom"];
	        this.canDeductInputVat = source["canDeductInputVat"];
	        this.startPeriod = source["startPeriod"];
	        this.periods = this.convertValues(source["periods"], PeriodInfo);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CategoryOption {
	    value: string;
	    label: string;
	    account: string;
	
	    static createFrom(source: any = {}) {
	        return new CategoryOption(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.value = source["value"];
	        this.label = source["label"];
	        this.account = source["account"];
	    }
	}
	export class ClaimItemView {
	    lineNo: number;
	    category: string;
	    categoryLabel: string;
	    occurDate: string;
	    summary: string;
	    amount: number;
	    taxAmount: number;
	    netAmount: number;
	    accountCode: string;
	    deptId?: number;
	
	    static createFrom(source: any = {}) {
	        return new ClaimItemView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.lineNo = source["lineNo"];
	        this.category = source["category"];
	        this.categoryLabel = source["categoryLabel"];
	        this.occurDate = source["occurDate"];
	        this.summary = source["summary"];
	        this.amount = source["amount"];
	        this.taxAmount = source["taxAmount"];
	        this.netAmount = source["netAmount"];
	        this.accountCode = source["accountCode"];
	        this.deptId = source["deptId"];
	    }
	}
	export class ClaimView {
	    id: number;
	    code: string;
	    claimantEmployeeId: number;
	    claimantName: string;
	    deptId?: number;
	    applyDate: string;
	    tripStart: string;
	    tripEnd: string;
	    destination: string;
	    reason: string;
	    status: string;
	    statusLabel: string;
	    totalAmount: number;
	    totalTax: number;
	    approverEmployeeId?: number;
	    approverName: string;
	    approvedAt: string;
	    payFromAccount: string;
	    remark: string;
	    items: ClaimItemView[];
	    voucherNo: string;
	    canEdit: boolean;
	    canApprove: boolean;
	    canPost: boolean;
	    blockedReason: string;
	
	    static createFrom(source: any = {}) {
	        return new ClaimView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.code = source["code"];
	        this.claimantEmployeeId = source["claimantEmployeeId"];
	        this.claimantName = source["claimantName"];
	        this.deptId = source["deptId"];
	        this.applyDate = source["applyDate"];
	        this.tripStart = source["tripStart"];
	        this.tripEnd = source["tripEnd"];
	        this.destination = source["destination"];
	        this.reason = source["reason"];
	        this.status = source["status"];
	        this.statusLabel = source["statusLabel"];
	        this.totalAmount = source["totalAmount"];
	        this.totalTax = source["totalTax"];
	        this.approverEmployeeId = source["approverEmployeeId"];
	        this.approverName = source["approverName"];
	        this.approvedAt = source["approvedAt"];
	        this.payFromAccount = source["payFromAccount"];
	        this.remark = source["remark"];
	        this.items = this.convertValues(source["items"], ClaimItemView);
	        this.voucherNo = source["voucherNo"];
	        this.canEdit = source["canEdit"];
	        this.canApprove = source["canApprove"];
	        this.canPost = source["canPost"];
	        this.blockedReason = source["blockedReason"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CloseResult {
	    period: string;
	    voucherNo: string;
	    voucherId: number;
	    voucherCreated: boolean;
	    summary: string;
	    health?: HealthInfo;
	
	    static createFrom(source: any = {}) {
	        return new CloseResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.period = source["period"];
	        this.voucherNo = source["voucherNo"];
	        this.voucherId = source["voucherId"];
	        this.voucherCreated = source["voucherCreated"];
	        this.summary = source["summary"];
	        this.health = this.convertValues(source["health"], HealthInfo);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class ClosingStep {
	    key: string;
	    title: string;
	    detail: string;
	    done: boolean;
	    skipped: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ClosingStep(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.title = source["title"];
	        this.detail = source["detail"];
	        this.done = source["done"];
	        this.skipped = source["skipped"];
	    }
	}
	export class ClosingPreview {
	    period: string;
	    year: number;
	    month: number;
	    steps: ClosingStep[];
	    income: number;
	    expense: number;
	    profit: number;
	    entries: ClosingEntry[];
	    health?: HealthInfo;
	
	    static createFrom(source: any = {}) {
	        return new ClosingPreview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.period = source["period"];
	        this.year = source["year"];
	        this.month = source["month"];
	        this.steps = this.convertValues(source["steps"], ClosingStep);
	        this.income = source["income"];
	        this.expense = source["expense"];
	        this.profit = source["profit"];
	        this.entries = this.convertValues(source["entries"], ClosingEntry);
	        this.health = this.convertValues(source["health"], HealthInfo);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class ColumnarColumnView {
	    key: string;
	    label: string;
	    side: string;
	    sideLabel: string;
	    accountCode?: string;
	    other: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ColumnarColumnView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.label = source["label"];
	        this.side = source["side"];
	        this.sideLabel = source["sideLabel"];
	        this.accountCode = source["accountCode"];
	        this.other = source["other"];
	    }
	}
	export class ColumnarExportResult {
	    path: string;
	    title: string;
	    sheetName: string;
	    rows: number;
	    columns: string[];
	
	    static createFrom(source: any = {}) {
	        return new ColumnarExportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.title = source["title"];
	        this.sheetName = source["sheetName"];
	        this.rows = source["rows"];
	        this.columns = source["columns"];
	    }
	}
	export class ColumnarRowView {
	    date: string;
	    voucherNo: string;
	    summary: string;
	    amounts: number[];
	    total: number;
	    debit: number;
	    credit: number;
	    balance: number;
	    dir: string;
	
	    static createFrom(source: any = {}) {
	        return new ColumnarRowView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.date = source["date"];
	        this.voucherNo = source["voucherNo"];
	        this.summary = source["summary"];
	        this.amounts = source["amounts"];
	        this.total = source["total"];
	        this.debit = source["debit"];
	        this.credit = source["credit"];
	        this.balance = source["balance"];
	        this.dir = source["dir"];
	    }
	}
	export class ColumnarView {
	    accountCode: string;
	    accountName: string;
	    from: string;
	    to: string;
	    columns: ColumnarColumnView[];
	    debitColumns: number[];
	    creditColumns: number[];
	    rows: ColumnarRowView[];
	    columnTotals: number[];
	    opening: number;
	    openingDir: string;
	    debitTotal: number;
	    creditTotal: number;
	    net: number;
	    closing: number;
	    closingDir: string;
	    summary: string;
	    notes: string[];
	
	    static createFrom(source: any = {}) {
	        return new ColumnarView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.accountCode = source["accountCode"];
	        this.accountName = source["accountName"];
	        this.from = source["from"];
	        this.to = source["to"];
	        this.columns = this.convertValues(source["columns"], ColumnarColumnView);
	        this.debitColumns = source["debitColumns"];
	        this.creditColumns = source["creditColumns"];
	        this.rows = this.convertValues(source["rows"], ColumnarRowView);
	        this.columnTotals = source["columnTotals"];
	        this.opening = source["opening"];
	        this.openingDir = source["openingDir"];
	        this.debitTotal = source["debitTotal"];
	        this.creditTotal = source["creditTotal"];
	        this.net = source["net"];
	        this.closing = source["closing"];
	        this.closingDir = source["closingDir"];
	        this.summary = source["summary"];
	        this.notes = source["notes"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ContactOption {
	    id: number;
	    name: string;
	    kind: string;
	    kindLabel: string;
	    shortName: string;
	
	    static createFrom(source: any = {}) {
	        return new ContactOption(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.kind = source["kind"];
	        this.kindLabel = source["kindLabel"];
	        this.shortName = source["shortName"];
	    }
	}
	export class CreateBookInput {
	    CompanyName: string;
	    CreditCode: string;
	    LegalPerson: string;
	    TaxType: string;
	    EnterpriseScale: string;
	    VATStatus: string;
	    VATStatusEffectiveFrom: string;
	    StartYear: number;
	    StartMonth: number;
	    ThroughYear: number;
	    CurrentYear: number;
	    CurrentMonth: number;
	
	    static createFrom(source: any = {}) {
	        return new CreateBookInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.CompanyName = source["CompanyName"];
	        this.CreditCode = source["CreditCode"];
	        this.LegalPerson = source["LegalPerson"];
	        this.TaxType = source["TaxType"];
	        this.EnterpriseScale = source["EnterpriseScale"];
	        this.VATStatus = source["VATStatus"];
	        this.VATStatusEffectiveFrom = source["VATStatusEffectiveFrom"];
	        this.StartYear = source["StartYear"];
	        this.StartMonth = source["StartMonth"];
	        this.ThroughYear = source["ThroughYear"];
	        this.CurrentYear = source["CurrentYear"];
	        this.CurrentMonth = source["CurrentMonth"];
	    }
	}
	export class Dashboard {
	    book?: BookInfo;
	    currentPeriod: string;
	    latestClosedPeriod: string;
	    assets: number;
	    liabilities: number;
	    equity: number;
	    periodIncome: number;
	    periodExpense: number;
	    periodProfit: number;
	    closingCash: number;
	    draftVouchers: number;
	    unpostedBankFlows: number;
	    missingAttachments: number;
	    balanceSheetIssues: string[];
	
	    static createFrom(source: any = {}) {
	        return new Dashboard(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.book = this.convertValues(source["book"], BookInfo);
	        this.currentPeriod = source["currentPeriod"];
	        this.latestClosedPeriod = source["latestClosedPeriod"];
	        this.assets = source["assets"];
	        this.liabilities = source["liabilities"];
	        this.equity = source["equity"];
	        this.periodIncome = source["periodIncome"];
	        this.periodExpense = source["periodExpense"];
	        this.periodProfit = source["periodProfit"];
	        this.closingCash = source["closingCash"];
	        this.draftVouchers = source["draftVouchers"];
	        this.unpostedBankFlows = source["unpostedBankFlows"];
	        this.missingAttachments = source["missingAttachments"];
	        this.balanceSheetIssues = source["balanceSheetIssues"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class DeductionRequest {
	    On: string;
	    Voucher: string;
	    TaxYuan: string;
	    Method: string;
	    forSimplifiedOrExempt: boolean;
	    abnormalLoss: boolean;
	    collectiveWelfare: boolean;
	    cateringRecreation: boolean;
	    loanInterest: boolean;
	    nonTaxableTransaction: boolean;
	    equityTransfer: boolean;
	    isLongTermAsset: boolean;
	    mixedUse: boolean;
	    assetValueYuan: string;
	    alreadyCredited: boolean;
	
	    static createFrom(source: any = {}) {
	        return new DeductionRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.On = source["On"];
	        this.Voucher = source["Voucher"];
	        this.TaxYuan = source["TaxYuan"];
	        this.Method = source["Method"];
	        this.forSimplifiedOrExempt = source["forSimplifiedOrExempt"];
	        this.abnormalLoss = source["abnormalLoss"];
	        this.collectiveWelfare = source["collectiveWelfare"];
	        this.cateringRecreation = source["cateringRecreation"];
	        this.loanInterest = source["loanInterest"];
	        this.nonTaxableTransaction = source["nonTaxableTransaction"];
	        this.equityTransfer = source["equityTransfer"];
	        this.isLongTermAsset = source["isLongTermAsset"];
	        this.mixedUse = source["mixedUse"];
	        this.assetValueYuan = source["assetValueYuan"];
	        this.alreadyCredited = source["alreadyCredited"];
	    }
	}
	export class DeductionView {
	    deductible: boolean;
	    reason: string;
	    reasonLabel: string;
	    deductibleAmount: number;
	    transferOut: number;
	    includedInCost: number;
	    note: string;
	    vatStatusLabel: string;
	    vatStatusOnDate: string;
	    identityKnown: boolean;
	    voucherLabel: string;
	    methodLabel: string;
	
	    static createFrom(source: any = {}) {
	        return new DeductionView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deductible = source["deductible"];
	        this.reason = source["reason"];
	        this.reasonLabel = source["reasonLabel"];
	        this.deductibleAmount = source["deductibleAmount"];
	        this.transferOut = source["transferOut"];
	        this.includedInCost = source["includedInCost"];
	        this.note = source["note"];
	        this.vatStatusLabel = source["vatStatusLabel"];
	        this.vatStatusOnDate = source["vatStatusOnDate"];
	        this.identityKnown = source["identityKnown"];
	        this.voucherLabel = source["voucherLabel"];
	        this.methodLabel = source["methodLabel"];
	    }
	}
	export class DemoResult {
	    companyName: string;
	    posted: number;
	    closedMonths: number;
	    toMonth: number;
	    warnings: string[];
	
	    static createFrom(source: any = {}) {
	        return new DemoResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.companyName = source["companyName"];
	        this.posted = source["posted"];
	        this.closedMonths = source["closedMonths"];
	        this.toMonth = source["toMonth"];
	        this.warnings = source["warnings"];
	    }
	}
	export class Department {
	    id: number;
	    code: string;
	    name: string;
	    parentId?: number;
	    enabled: boolean;
	    remark: string;
	    fullName: string;
	
	    static createFrom(source: any = {}) {
	        return new Department(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.code = source["code"];
	        this.name = source["name"];
	        this.parentId = source["parentId"];
	        this.enabled = source["enabled"];
	        this.remark = source["remark"];
	        this.fullName = source["fullName"];
	    }
	}
	export class DepartmentUsage {
	    employees: number;
	    children: number;
	    entries: number;
	    bankFlows: number;
	    bankRules: number;
	    claims: number;
	
	    static createFrom(source: any = {}) {
	        return new DepartmentUsage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.employees = source["employees"];
	        this.children = source["children"];
	        this.entries = source["entries"];
	        this.bankFlows = source["bankFlows"];
	        this.bankRules = source["bankRules"];
	        this.claims = source["claims"];
	    }
	}
	export class EmployeeView {
	    id: number;
	    code: string;
	    name: string;
	    idCard: string;
	    phone: string;
	    bankName: string;
	    bankAccount: string;
	    baseSalary: number;
	    siBase: number;
	    hfbBase: number;
	    siProfile: string;
	    specialAdditional: number;
	    deptId?: number;
	    position: string;
	    expenseAccountCode: string;
	    expenseAccountName: string;
	    hireDate: string;
	    leaveDate: string;
	    enabled: boolean;
	    remark: string;
	    kind: string;
	    kindLabel: string;
	    statusLabel: string;
	
	    static createFrom(source: any = {}) {
	        return new EmployeeView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.code = source["code"];
	        this.name = source["name"];
	        this.idCard = source["idCard"];
	        this.phone = source["phone"];
	        this.bankName = source["bankName"];
	        this.bankAccount = source["bankAccount"];
	        this.baseSalary = source["baseSalary"];
	        this.siBase = source["siBase"];
	        this.hfbBase = source["hfbBase"];
	        this.siProfile = source["siProfile"];
	        this.specialAdditional = source["specialAdditional"];
	        this.deptId = source["deptId"];
	        this.position = source["position"];
	        this.expenseAccountCode = source["expenseAccountCode"];
	        this.expenseAccountName = source["expenseAccountName"];
	        this.hireDate = source["hireDate"];
	        this.leaveDate = source["leaveDate"];
	        this.enabled = source["enabled"];
	        this.remark = source["remark"];
	        this.kind = source["kind"];
	        this.kindLabel = source["kindLabel"];
	        this.statusLabel = source["statusLabel"];
	    }
	}
	export class ExportResult {
	    path: string;
	    kind: string;
	    title: string;
	    rows: number;
	    sheetName: string;
	    issues: number;
	
	    static createFrom(source: any = {}) {
	        return new ExportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.kind = source["kind"];
	        this.title = source["title"];
	        this.rows = source["rows"];
	        this.sheetName = source["sheetName"];
	        this.issues = source["issues"];
	    }
	}
	
	
	export class InsuranceSchemeInfo {
	    scheme?: payroll.InsuranceScheme;
	    usedBy: number;
	    configured: boolean;
	
	    static createFrom(source: any = {}) {
	        return new InsuranceSchemeInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.scheme = this.convertValues(source["scheme"], payroll.InsuranceScheme);
	        this.usedBy = source["usedBy"];
	        this.configured = source["configured"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class InvoiceSummaryView {
	    count: number;
	    amountExTax: number;
	    taxAmount: number;
	    totalAmount: number;
	    deductible: number;
	    unpostedCount: number;
	
	    static createFrom(source: any = {}) {
	        return new InvoiceSummaryView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.count = source["count"];
	        this.amountExTax = source["amountExTax"];
	        this.taxAmount = source["taxAmount"];
	        this.totalAmount = source["totalAmount"];
	        this.deductible = source["deductible"];
	        this.unpostedCount = source["unpostedCount"];
	    }
	}
	export class InvoiceView {
	    id: number;
	    direction: string;
	    directionLabel: string;
	    kind: string;
	    kindLabel: string;
	    code: string;
	    number: string;
	    invoiceDate: string;
	    sellerName: string;
	    buyerName: string;
	    amountExTax: number;
	    taxRatePpm: number;
	    taxRateLabel: string;
	    taxAmount: number;
	    totalAmount: number;
	    deductibleTax: number;
	    costAmount: number;
	    category: string;
	    status: string;
	    statusLabel: string;
	    contactId?: number;
	    remark: string;
	    voucherNo: string;
	    posted: boolean;
	    attachmentCount: number;
	
	    static createFrom(source: any = {}) {
	        return new InvoiceView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.direction = source["direction"];
	        this.directionLabel = source["directionLabel"];
	        this.kind = source["kind"];
	        this.kindLabel = source["kindLabel"];
	        this.code = source["code"];
	        this.number = source["number"];
	        this.invoiceDate = source["invoiceDate"];
	        this.sellerName = source["sellerName"];
	        this.buyerName = source["buyerName"];
	        this.amountExTax = source["amountExTax"];
	        this.taxRatePpm = source["taxRatePpm"];
	        this.taxRateLabel = source["taxRateLabel"];
	        this.taxAmount = source["taxAmount"];
	        this.totalAmount = source["totalAmount"];
	        this.deductibleTax = source["deductibleTax"];
	        this.costAmount = source["costAmount"];
	        this.category = source["category"];
	        this.status = source["status"];
	        this.statusLabel = source["statusLabel"];
	        this.contactId = source["contactId"];
	        this.remark = source["remark"];
	        this.voucherNo = source["voucherNo"];
	        this.posted = source["posted"];
	        this.attachmentCount = source["attachmentCount"];
	    }
	}
	export class LogSettings {
	    recordViews: boolean;
	    viewIntervalSeconds: number;
	
	    static createFrom(source: any = {}) {
	        return new LogSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.recordViews = source["recordViews"];
	        this.viewIntervalSeconds = source["viewIntervalSeconds"];
	    }
	}
	export class PayrollItemView {
	    employeeId: number;
	    employeeName: string;
	    gross: number;
	    attendanceDeduct: number;
	    otherDeduct: number;
	    insuranceBase: number;
	    housingFundBase: number;
	    insuranceSelf: number;
	    insuranceCompany: number;
	    specialAdditional: number;
	    taxableIncome: number;
	    taxWarning: string;
	    iit: number;
	    net: number;
	
	    static createFrom(source: any = {}) {
	        return new PayrollItemView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.employeeId = source["employeeId"];
	        this.employeeName = source["employeeName"];
	        this.gross = source["gross"];
	        this.attendanceDeduct = source["attendanceDeduct"];
	        this.otherDeduct = source["otherDeduct"];
	        this.insuranceBase = source["insuranceBase"];
	        this.housingFundBase = source["housingFundBase"];
	        this.insuranceSelf = source["insuranceSelf"];
	        this.insuranceCompany = source["insuranceCompany"];
	        this.specialAdditional = source["specialAdditional"];
	        this.taxableIncome = source["taxableIncome"];
	        this.taxWarning = source["taxWarning"];
	        this.iit = source["iit"];
	        this.net = source["net"];
	    }
	}
	export class PayrollRunDetail {
	    id: number;
	    period: string;
	    status: string;
	    statusLabel: string;
	    taxNote: string;
	    items: PayrollItemView[];
	    headcount: number;
	    totalGross: number;
	    totalIit: number;
	    totalSiSelf: number;
	    totalSiCompany: number;
	    totalNet: number;
	    accrualVoucherNo: string;
	    paymentVoucherNo: string;
	    canConfirm: boolean;
	    canPost: boolean;
	
	    static createFrom(source: any = {}) {
	        return new PayrollRunDetail(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.period = source["period"];
	        this.status = source["status"];
	        this.statusLabel = source["statusLabel"];
	        this.taxNote = source["taxNote"];
	        this.items = this.convertValues(source["items"], PayrollItemView);
	        this.headcount = source["headcount"];
	        this.totalGross = source["totalGross"];
	        this.totalIit = source["totalIit"];
	        this.totalSiSelf = source["totalSiSelf"];
	        this.totalSiCompany = source["totalSiCompany"];
	        this.totalNet = source["totalNet"];
	        this.accrualVoucherNo = source["accrualVoucherNo"];
	        this.paymentVoucherNo = source["paymentVoucherNo"];
	        this.canConfirm = source["canConfirm"];
	        this.canPost = source["canPost"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PayrollRunView {
	    id: number;
	    period: string;
	    status: string;
	    statusLabel: string;
	    headcount: number;
	    totalGross: number;
	    totalIit: number;
	    totalSiSelf: number;
	    totalSiCompany: number;
	    totalNet: number;
	    hasAccrualVoucher: boolean;
	    hasPaymentVoucher: boolean;
	    createdBy: string;
	
	    static createFrom(source: any = {}) {
	        return new PayrollRunView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.period = source["period"];
	        this.status = source["status"];
	        this.statusLabel = source["statusLabel"];
	        this.headcount = source["headcount"];
	        this.totalGross = source["totalGross"];
	        this.totalIit = source["totalIit"];
	        this.totalSiSelf = source["totalSiSelf"];
	        this.totalSiCompany = source["totalSiCompany"];
	        this.totalNet = source["totalNet"];
	        this.hasAccrualVoucher = source["hasAccrualVoucher"];
	        this.hasPaymentVoucher = source["hasPaymentVoucher"];
	        this.createdBy = source["createdBy"];
	    }
	}
	
	export class ReconciliationItemView {
	    date: string;
	    days: number;
	    summary: string;
	    reference: string;
	    amount: number;
	
	    static createFrom(source: any = {}) {
	        return new ReconciliationItemView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.date = source["date"];
	        this.days = source["days"];
	        this.summary = source["summary"];
	        this.reference = source["reference"];
	        this.amount = source["amount"];
	    }
	}
	export class ReconciliationLineView {
	    side: string;
	    kind: string;
	    label: string;
	    amount: number;
	    subtotal: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ReconciliationLineView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.side = source["side"];
	        this.kind = source["kind"];
	        this.label = source["label"];
	        this.amount = source["amount"];
	        this.subtotal = source["subtotal"];
	    }
	}
	export class ReconciliationView {
	    accountCode: string;
	    accountName: string;
	    asOf: string;
	    from: string;
	    bookOpening: number;
	    bankOpening?: number;
	    openingDiff: number;
	    bookBalance: number;
	    bookAdjusted: number;
	    bankBalance?: number;
	    bankAdjusted?: number;
	    bankReceivedNotBooked: ReconciliationItemView[];
	    bankPaidNotBooked: ReconciliationItemView[];
	    bookReceivedNotBanked: ReconciliationItemView[];
	    bookPaidNotBanked: ReconciliationItemView[];
	    lines: ReconciliationLineView[];
	    unreconciledFlows: number;
	    status: string;
	    statusLabel: string;
	    balanced: boolean;
	    difference: number;
	    summary: string;
	    notes: string[];
	
	    static createFrom(source: any = {}) {
	        return new ReconciliationView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.accountCode = source["accountCode"];
	        this.accountName = source["accountName"];
	        this.asOf = source["asOf"];
	        this.from = source["from"];
	        this.bookOpening = source["bookOpening"];
	        this.bankOpening = source["bankOpening"];
	        this.openingDiff = source["openingDiff"];
	        this.bookBalance = source["bookBalance"];
	        this.bookAdjusted = source["bookAdjusted"];
	        this.bankBalance = source["bankBalance"];
	        this.bankAdjusted = source["bankAdjusted"];
	        this.bankReceivedNotBooked = this.convertValues(source["bankReceivedNotBooked"], ReconciliationItemView);
	        this.bankPaidNotBooked = this.convertValues(source["bankPaidNotBooked"], ReconciliationItemView);
	        this.bookReceivedNotBanked = this.convertValues(source["bookReceivedNotBanked"], ReconciliationItemView);
	        this.bookPaidNotBanked = this.convertValues(source["bookPaidNotBanked"], ReconciliationItemView);
	        this.lines = this.convertValues(source["lines"], ReconciliationLineView);
	        this.unreconciledFlows = source["unreconciledFlows"];
	        this.status = source["status"];
	        this.statusLabel = source["statusLabel"];
	        this.balanced = source["balanced"];
	        this.difference = source["difference"];
	        this.summary = source["summary"];
	        this.notes = source["notes"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ReopenResult {
	    period: string;
	    reversed: string[];
	    voucherIds: number[];
	
	    static createFrom(source: any = {}) {
	        return new ReopenResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.period = source["period"];
	        this.reversed = source["reversed"];
	        this.voucherIds = source["voucherIds"];
	    }
	}
	export class StatementLineView {
	    date: string;
	    voucherNo: string;
	    summary: string;
	    debit: number;
	    credit: number;
	    balance: number;
	    dir: string;
	
	    static createFrom(source: any = {}) {
	        return new StatementLineView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.date = source["date"];
	        this.voucherNo = source["voucherNo"];
	        this.summary = source["summary"];
	        this.debit = source["debit"];
	        this.credit = source["credit"];
	        this.balance = source["balance"];
	        this.dir = source["dir"];
	    }
	}
	export class StatementView {
	    companyName: string;
	    contactId: number;
	    contactName: string;
	    contactKind: string;
	    contactKindLabel: string;
	    contactTaxNo: string;
	    contactAddress: string;
	    from: string;
	    to: string;
	    accountCode?: string;
	    accountName?: string;
	    mixed: boolean;
	    opening: number;
	    openingDir: string;
	    lines: StatementLineView[];
	    totalDebit: number;
	    totalCredit: number;
	    closing: number;
	    closingDir: string;
	    closingUpper: string;
	    summary: string;
	    text: string;
	
	    static createFrom(source: any = {}) {
	        return new StatementView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.companyName = source["companyName"];
	        this.contactId = source["contactId"];
	        this.contactName = source["contactName"];
	        this.contactKind = source["contactKind"];
	        this.contactKindLabel = source["contactKindLabel"];
	        this.contactTaxNo = source["contactTaxNo"];
	        this.contactAddress = source["contactAddress"];
	        this.from = source["from"];
	        this.to = source["to"];
	        this.accountCode = source["accountCode"];
	        this.accountName = source["accountName"];
	        this.mixed = source["mixed"];
	        this.opening = source["opening"];
	        this.openingDir = source["openingDir"];
	        this.lines = this.convertValues(source["lines"], StatementLineView);
	        this.totalDebit = source["totalDebit"];
	        this.totalCredit = source["totalCredit"];
	        this.closing = source["closing"];
	        this.closingDir = source["closingDir"];
	        this.closingUpper = source["closingUpper"];
	        this.summary = source["summary"];
	        this.text = source["text"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SummaryAccountView {
	    accountCode: string;
	    accountName: string;
	    fullName: string;
	    count: number;
	    debit: number;
	    credit: number;
	    net: number;
	    dir: string;
	
	    static createFrom(source: any = {}) {
	        return new SummaryAccountView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.accountCode = source["accountCode"];
	        this.accountName = source["accountName"];
	        this.fullName = source["fullName"];
	        this.count = source["count"];
	        this.debit = source["debit"];
	        this.credit = source["credit"];
	        this.net = source["net"];
	        this.dir = source["dir"];
	    }
	}
	export class SummaryDayView {
	    date: string;
	    count: number;
	    nos: string[];
	    debit: number;
	    credit: number;
	
	    static createFrom(source: any = {}) {
	        return new SummaryDayView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.date = source["date"];
	        this.count = source["count"];
	        this.nos = source["nos"];
	        this.debit = source["debit"];
	        this.credit = source["credit"];
	    }
	}
	export class SummaryExportResult {
	    path: string;
	    title: string;
	    sheetName: string;
	    rows: number;
	
	    static createFrom(source: any = {}) {
	        return new SummaryExportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.title = source["title"];
	        this.sheetName = source["sheetName"];
	        this.rows = source["rows"];
	    }
	}
	export class SummaryWordView {
	    word: string;
	    label: string;
	    count: number;
	    debit: number;
	    credit: number;
	
	    static createFrom(source: any = {}) {
	        return new SummaryWordView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.word = source["word"];
	        this.label = source["label"];
	        this.count = source["count"];
	        this.debit = source["debit"];
	        this.credit = source["credit"];
	    }
	}
	export class SummaryView {
	    from: string;
	    to: string;
	    wordRows: SummaryWordView[];
	    dayRows: SummaryDayView[];
	    accountRows: SummaryAccountView[];
	    voucherCount: number;
	    debitTotal: number;
	    creditTotal: number;
	    balanced: boolean;
	    draftCount: number;
	    voidedCount: number;
	    reversalCount: number;
	    attachmentTotal: number;
	    gapDays: number;
	    summary: string;
	    notes: string[];
	
	    static createFrom(source: any = {}) {
	        return new SummaryView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.from = source["from"];
	        this.to = source["to"];
	        this.wordRows = this.convertValues(source["wordRows"], SummaryWordView);
	        this.dayRows = this.convertValues(source["dayRows"], SummaryDayView);
	        this.accountRows = this.convertValues(source["accountRows"], SummaryAccountView);
	        this.voucherCount = source["voucherCount"];
	        this.debitTotal = source["debitTotal"];
	        this.creditTotal = source["creditTotal"];
	        this.balanced = source["balanced"];
	        this.draftCount = source["draftCount"];
	        this.voidedCount = source["voidedCount"];
	        this.reversalCount = source["reversalCount"];
	        this.attachmentTotal = source["attachmentTotal"];
	        this.gapDays = source["gapDays"];
	        this.summary = source["summary"];
	        this.notes = source["notes"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class TaxBracketInfo {
	    upper: number;
	    ratePpm: number;
	    rateLabel: string;
	    deduction: number;
	
	    static createFrom(source: any = {}) {
	        return new TaxBracketInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.upper = source["upper"];
	        this.ratePpm = source["ratePpm"];
	        this.rateLabel = source["rateLabel"];
	        this.deduction = source["deduction"];
	    }
	}
	export class TaxRateOption {
	    ppm: number;
	    label: string;
	
	    static createFrom(source: any = {}) {
	        return new TaxRateOption(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ppm = source["ppm"];
	        this.label = source["label"];
	    }
	}
	export class TaxTableInfo {
	    name: string;
	    brackets: TaxBracketInfo[];
	    note: string;
	
	    static createFrom(source: any = {}) {
	        return new TaxTableInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.brackets = this.convertValues(source["brackets"], TaxBracketInfo);
	        this.note = source["note"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class VATStatusPeriodView {
	    status: string;
	    statusLabel: string;
	    from: string;
	    to: string;
	    note: string;
	
	    static createFrom(source: any = {}) {
	        return new VATStatusPeriodView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.statusLabel = source["statusLabel"];
	        this.from = source["from"];
	        this.to = source["to"];
	        this.note = source["note"];
	    }
	}
	export class VATIdentityInfo {
	    vatStatus: string;
	    vatStatusLabel: string;
	    vatStatusEffectiveFrom: string;
	    canDeductInputVat: boolean;
	    statusHistory: VATStatusPeriodView[];
	    enterpriseScale: string;
	    enterpriseScaleLabel: string;
	    enterpriseScaleSet: boolean;
	
	    static createFrom(source: any = {}) {
	        return new VATIdentityInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.vatStatus = source["vatStatus"];
	        this.vatStatusLabel = source["vatStatusLabel"];
	        this.vatStatusEffectiveFrom = source["vatStatusEffectiveFrom"];
	        this.canDeductInputVat = source["canDeductInputVat"];
	        this.statusHistory = this.convertValues(source["statusHistory"], VATStatusPeriodView);
	        this.enterpriseScale = source["enterpriseScale"];
	        this.enterpriseScaleLabel = source["enterpriseScaleLabel"];
	        this.enterpriseScaleSet = source["enterpriseScaleSet"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class VATOption {
	    value: string;
	    label: string;
	    deductible: boolean;
	    transferOut: boolean;
	
	    static createFrom(source: any = {}) {
	        return new VATOption(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.value = source["value"];
	        this.label = source["label"];
	        this.deductible = source["deductible"];
	        this.transferOut = source["transferOut"];
	    }
	}
	export class VATPolicyStatus {
	    version: string;
	    builtinVersion: string;
	    origin: string;
	    builtin: boolean;
	    updateAvailable: boolean;
	
	    static createFrom(source: any = {}) {
	        return new VATPolicyStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.builtinVersion = source["builtinVersion"];
	        this.origin = source["origin"];
	        this.builtin = source["builtin"];
	        this.updateAvailable = source["updateAvailable"];
	    }
	}
	export class VATPolicyView {
	    code: string;
	    name: string;
	    category: string;
	    categoryLabel: string;
	    status: string;
	    statusLabel: string;
	    subject: string;
	    subjectLabel: string;
	    method: string;
	    methodLabel: string;
	    treatment: string;
	    treatmentLabel: string;
	    inputTax: string;
	    inputTaxLabel: string;
	    statutoryRate: string;
	    preferentialRate: string;
	    rate: string;
	    rateApplicable: boolean;
	    hasPreference: boolean;
	    conditional: boolean;
	    exact: boolean;
	    note: string;
	    effectiveFrom: string;
	    effectiveTo: string;
	    activeOn: boolean;
	    expiresOn: string;
	    legalBasis: string;
	    version: string;
	
	    static createFrom(source: any = {}) {
	        return new VATPolicyView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.name = source["name"];
	        this.category = source["category"];
	        this.categoryLabel = source["categoryLabel"];
	        this.status = source["status"];
	        this.statusLabel = source["statusLabel"];
	        this.subject = source["subject"];
	        this.subjectLabel = source["subjectLabel"];
	        this.method = source["method"];
	        this.methodLabel = source["methodLabel"];
	        this.treatment = source["treatment"];
	        this.treatmentLabel = source["treatmentLabel"];
	        this.inputTax = source["inputTax"];
	        this.inputTaxLabel = source["inputTaxLabel"];
	        this.statutoryRate = source["statutoryRate"];
	        this.preferentialRate = source["preferentialRate"];
	        this.rate = source["rate"];
	        this.rateApplicable = source["rateApplicable"];
	        this.hasPreference = source["hasPreference"];
	        this.conditional = source["conditional"];
	        this.exact = source["exact"];
	        this.note = source["note"];
	        this.effectiveFrom = source["effectiveFrom"];
	        this.effectiveTo = source["effectiveTo"];
	        this.activeOn = source["activeOn"];
	        this.expiresOn = source["expiresOn"];
	        this.legalBasis = source["legalBasis"];
	        this.version = source["version"];
	    }
	}
	export class VATRateResult {
	    code: string;
	    name: string;
	    category: string;
	    categoryLabel: string;
	    status: string;
	    statusLabel: string;
	    subject: string;
	    subjectLabel: string;
	    method: string;
	    methodLabel: string;
	    treatment: string;
	    treatmentLabel: string;
	    inputTax: string;
	    inputTaxLabel: string;
	    statutoryRate: string;
	    preferentialRate: string;
	    rate: string;
	    rateApplicable: boolean;
	    hasPreference: boolean;
	    conditional: boolean;
	    exact: boolean;
	    note: string;
	    effectiveFrom: string;
	    effectiveTo: string;
	    activeOn: boolean;
	    expiresOn: string;
	    legalBasis: string;
	    version: string;
	
	    static createFrom(source: any = {}) {
	        return new VATRateResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.name = source["name"];
	        this.category = source["category"];
	        this.categoryLabel = source["categoryLabel"];
	        this.status = source["status"];
	        this.statusLabel = source["statusLabel"];
	        this.subject = source["subject"];
	        this.subjectLabel = source["subjectLabel"];
	        this.method = source["method"];
	        this.methodLabel = source["methodLabel"];
	        this.treatment = source["treatment"];
	        this.treatmentLabel = source["treatmentLabel"];
	        this.inputTax = source["inputTax"];
	        this.inputTaxLabel = source["inputTaxLabel"];
	        this.statutoryRate = source["statutoryRate"];
	        this.preferentialRate = source["preferentialRate"];
	        this.rate = source["rate"];
	        this.rateApplicable = source["rateApplicable"];
	        this.hasPreference = source["hasPreference"];
	        this.conditional = source["conditional"];
	        this.exact = source["exact"];
	        this.note = source["note"];
	        this.effectiveFrom = source["effectiveFrom"];
	        this.effectiveTo = source["effectiveTo"];
	        this.activeOn = source["activeOn"];
	        this.expiresOn = source["expiresOn"];
	        this.legalBasis = source["legalBasis"];
	        this.version = source["version"];
	    }
	}
	export class VATReferenceInfo {
	    voucherKinds: VATOption[];
	    nonDeductibleReasons: VATOption[];
	    categories: VATOption[];
	    methods: VATOption[];
	    softwareHint: string;
	    longTermAssetThreshold: number;
	
	    static createFrom(source: any = {}) {
	        return new VATReferenceInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.voucherKinds = this.convertValues(source["voucherKinds"], VATOption);
	        this.nonDeductibleReasons = this.convertValues(source["nonDeductibleReasons"], VATOption);
	        this.categories = this.convertValues(source["categories"], VATOption);
	        this.methods = this.convertValues(source["methods"], VATOption);
	        this.softwareHint = source["softwareHint"];
	        this.longTermAssetThreshold = source["longTermAssetThreshold"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class VoucherLine {
	    lineNo: number;
	    accountCode: string;
	    accountName: string;
	    summary: string;
	    debit: number;
	    credit: number;
	    auxDesc: string;
	
	    static createFrom(source: any = {}) {
	        return new VoucherLine(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.lineNo = source["lineNo"];
	        this.accountCode = source["accountCode"];
	        this.accountName = source["accountName"];
	        this.summary = source["summary"];
	        this.debit = source["debit"];
	        this.credit = source["credit"];
	        this.auxDesc = source["auxDesc"];
	    }
	}
	export class VoucherDetail {
	    id: number;
	    no: string;
	    word: string;
	    date: string;
	    remark: string;
	    status: string;
	    statusLabel: string;
	    attachCount: number;
	    source: string;
	    sourceLabel: string;
	    createdByAi: boolean;
	    createdBy: string;
	    reviewedBy: string;
	    postedBy: string;
	    postedAt: string;
	    reversesNo: string;
	    voidedByNo: string;
	    lines: VoucherLine[];
	    totalDebit: number;
	    totalCredit: number;
	    balanced: boolean;
	    canEdit: boolean;
	    canDelete: boolean;
	    canPost: boolean;
	    canReverse: boolean;
	    blockedReason: string;
	
	    static createFrom(source: any = {}) {
	        return new VoucherDetail(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.no = source["no"];
	        this.word = source["word"];
	        this.date = source["date"];
	        this.remark = source["remark"];
	        this.status = source["status"];
	        this.statusLabel = source["statusLabel"];
	        this.attachCount = source["attachCount"];
	        this.source = source["source"];
	        this.sourceLabel = source["sourceLabel"];
	        this.createdByAi = source["createdByAi"];
	        this.createdBy = source["createdBy"];
	        this.reviewedBy = source["reviewedBy"];
	        this.postedBy = source["postedBy"];
	        this.postedAt = source["postedAt"];
	        this.reversesNo = source["reversesNo"];
	        this.voidedByNo = source["voidedByNo"];
	        this.lines = this.convertValues(source["lines"], VoucherLine);
	        this.totalDebit = source["totalDebit"];
	        this.totalCredit = source["totalCredit"];
	        this.balanced = source["balanced"];
	        this.canEdit = source["canEdit"];
	        this.canDelete = source["canDelete"];
	        this.canPost = source["canPost"];
	        this.canReverse = source["canReverse"];
	        this.blockedReason = source["blockedReason"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	export class VoucherSummary {
	    id: number;
	    no: string;
	    word: string;
	    date: string;
	    remark: string;
	    status: string;
	    statusLabel: string;
	    attachCount: number;
	    amount: number;
	    source: string;
	    sourceLabel: string;
	    createdByAi: boolean;
	    createdBy: string;
	    postedBy: string;
	    lines: number;
	
	    static createFrom(source: any = {}) {
	        return new VoucherSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.no = source["no"];
	        this.word = source["word"];
	        this.date = source["date"];
	        this.remark = source["remark"];
	        this.status = source["status"];
	        this.statusLabel = source["statusLabel"];
	        this.attachCount = source["attachCount"];
	        this.amount = source["amount"];
	        this.source = source["source"];
	        this.sourceLabel = source["sourceLabel"];
	        this.createdByAi = source["createdByAi"];
	        this.createdBy = source["createdBy"];
	        this.postedBy = source["postedBy"];
	        this.lines = source["lines"];
	    }
	}

}

export namespace sqlite {
	
	export class AIProviderConfig {
	    id: number;
	    name: string;
	    kind: string;
	    baseUrl: string;
	    model: string;
	    apiKey: string;
	    enabled: boolean;
	    isDefault: boolean;
	    timeoutMs: number;
	    remark: string;
	
	    static createFrom(source: any = {}) {
	        return new AIProviderConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.kind = source["kind"];
	        this.baseUrl = source["baseUrl"];
	        this.model = source["model"];
	        this.apiKey = source["apiKey"];
	        this.enabled = source["enabled"];
	        this.isDefault = source["isDefault"];
	        this.timeoutMs = source["timeoutMs"];
	        this.remark = source["remark"];
	    }
	}
	export class AIStats {
	    total: number;
	    proposed: number;
	    valid: number;
	    accepted: number;
	    modified: number;
	    rejected: number;
	    tokensIn: number;
	    tokensOut: number;
	
	    static createFrom(source: any = {}) {
	        return new AIStats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.total = source["total"];
	        this.proposed = source["proposed"];
	        this.valid = source["valid"];
	        this.accepted = source["accepted"];
	        this.modified = source["modified"];
	        this.rejected = source["rejected"];
	        this.tokensIn = source["tokensIn"];
	        this.tokensOut = source["tokensOut"];
	    }
	}
	export class BookPeek {
	    path: string;
	    fileName: string;
	    isBook: boolean;
	    companyName: string;
	    creditCode: string;
	    vatStatus: string;
	    startPeriod: string;
	    voucherCount: number;
	    sizeBytes: number;
	    modTime: string;
	    err?: string;
	
	    static createFrom(source: any = {}) {
	        return new BookPeek(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.fileName = source["fileName"];
	        this.isBook = source["isBook"];
	        this.companyName = source["companyName"];
	        this.creditCode = source["creditCode"];
	        this.vatStatus = source["vatStatus"];
	        this.startPeriod = source["startPeriod"];
	        this.voucherCount = source["voucherCount"];
	        this.sizeBytes = source["sizeBytes"];
	        this.modTime = source["modTime"];
	        this.err = source["err"];
	    }
	}
	export class SuggestionRow {
	    id: number;
	    targetType: string;
	    targetId?: number;
	    providerName: string;
	    model: string;
	    layer: string;
	    checksum: string;
	    confidence: number;
	    status: string;
	    rejectReason: string;
	    decision: string;
	    finalVoucher?: number;
	    tokensIn: number;
	    tokensOut: number;
	    createdAt: string;
	    decidedAt?: string;
	
	    static createFrom(source: any = {}) {
	        return new SuggestionRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.targetType = source["targetType"];
	        this.targetId = source["targetId"];
	        this.providerName = source["providerName"];
	        this.model = source["model"];
	        this.layer = source["layer"];
	        this.checksum = source["checksum"];
	        this.confidence = source["confidence"];
	        this.status = source["status"];
	        this.rejectReason = source["rejectReason"];
	        this.decision = source["decision"];
	        this.finalVoucher = source["finalVoucher"];
	        this.tokensIn = source["tokensIn"];
	        this.tokensOut = source["tokensOut"];
	        this.createdAt = source["createdAt"];
	        this.decidedAt = source["decidedAt"];
	    }
	}

}

