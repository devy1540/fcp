const state = {
  data: null,
  activeService: "overview",
  activeProvider: "ALL",
  query: "",
  loading: false,
  detailLoading: false,
  detailError: "",
  page: { service: "", resources: [], total: 0, offset: 0, limit: 25, hasPrevious: false, hasNext: false, query: "" },
  requestSerial: 0,
  refreshTimer: null,
  searchTimer: null,
  lastSuccessAt: 0,
  failures: 0,
  openVerification: new Set(),
  actioning: false,
  pendingAction: null,
  actionTrigger: null,
  createService: null,
  createTrigger: null,
  toastTimer: null,
}

const elements = {
  connection: document.querySelector("#connection-status"),
  connectionLabel: document.querySelector("#connection-label"),
  project: document.querySelector("#project-name"),
  updatedAt: document.querySelector("#updated-at"),
  summary: document.querySelector("#summary-grid"),
  serviceTotal: document.querySelector("#service-total"),
  providerFilter: document.querySelector("#provider-filter"),
  nav: document.querySelector("#service-nav"),
  kicker: document.querySelector("#section-kicker"),
  title: document.querySelector("#section-title"),
  description: document.querySelector("#section-description"),
  verification: document.querySelector("#verification-note"),
  status: document.querySelector("#panel-status"),
  content: document.querySelector("#resource-content"),
  search: document.querySelector("#resource-search"),
  refresh: document.querySelector("#refresh-button"),
  resetWorkload: document.querySelector("#reset-workload-button"),
  serviceActions: document.querySelector("#service-actions"),
  dialog: document.querySelector("#confirm-dialog"),
  dialogTitle: document.querySelector("#confirm-title"),
  dialogDescription: document.querySelector("#confirm-description"),
  dialogCancel: document.querySelector("#dialog-cancel"),
  dialogConfirm: document.querySelector("#dialog-confirm"),
  createDialog: document.querySelector("#create-dialog"),
  createForm: document.querySelector("#create-form"),
  createTitle: document.querySelector("#create-title"),
  createDescription: document.querySelector("#create-description"),
  createFields: document.querySelector("#create-fields"),
  createError: document.querySelector("#create-error"),
  createCancel: document.querySelector("#create-cancel"),
  createSubmit: document.querySelector("#create-submit"),
  toast: document.querySelector("#toast"),
}

const AUTO_REFRESH_MS = 3000
const STALE_AFTER_MS = 10000

const creatableServices = {
  s3: { label: "S3 버킷", kind: "bucket" },
  sqs: { label: "SQS 큐", kind: "queue" },
  dynamodb: { label: "DynamoDB 테이블", kind: "table" },
  gcs: { label: "Cloud Storage 버킷", kind: "bucket" },
  pubsub: { label: "Pub/Sub 리소스", kind: "topic" },
}

const serviceMarks = {
  overview: "OV",
  s3: "S3",
  sqs: "SQ",
  dynamodb: "DB",
  sts: "ST",
  gcs: "CS",
  pubsub: "PS",
  firestore: "FS",
  secrets: "SM",
  kms: "KM",
  iam: "IA",
  fcm: "FC",
  metadata: "MD",
  vertex: "VX",
}

function createElement(tag, className, text) {
  const element = document.createElement(tag)
  if (className) element.className = className
  if (text !== undefined) element.textContent = text
  return element
}

function clear(element) {
  while (element.firstChild) element.removeChild(element.firstChild)
}

function compactNumber(value) {
  return new Intl.NumberFormat("ko-KR", { notation: value >= 10000 ? "compact" : "standard" }).format(value)
}

function formatBytes(value) {
  const bytes = Number(value)
  if (!Number.isFinite(bytes) || bytes < 0) return value
  if (bytes < 1024) return `${bytes} B`
  const units = ["KB", "MB", "GB", "TB"]
  let size = bytes / 1024
  let unit = units[0]
  for (let index = 1; size >= 1024 && index < units.length; index += 1) {
    size /= 1024
    unit = units[index]
  }
  return `${size >= 10 ? size.toFixed(0) : size.toFixed(1)} ${unit}`
}

function formatDate(value) {
  if (!value) return ""
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ""
  return new Intl.DateTimeFormat("ko-KR", {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(date)
}

function shortName(value) {
  const parts = value.split("/").filter(Boolean)
  return parts.at(-1) || value
}

function searchableText(service, resource) {
  const attributes = resource.attributes?.flatMap((attribute) => [attribute.label, attribute.value]) ?? []
  return [service.name, service.provider, service.verification.label, service.verification.evidence, resource.name, resource.kind, resource.status, ...attributes]
    .join(" ")
    .toLocaleLowerCase("ko-KR")
}

function matchesQuery(service, resource) {
  const query = state.query.trim().toLocaleLowerCase("ko-KR")
  return !query || searchableText(service, resource).includes(query)
}

function serviceResourceCount(service) {
  return Number.isFinite(service.resourceCount) ? service.resourceCount : (service.resources?.length ?? 0)
}

function serviceMatchesQuery(service) {
  const query = state.query.trim().toLocaleLowerCase("ko-KR")
  if (!query) return true
  const operations = service.verification.operations?.flatMap((operation) => [operation.name, operation.status, operation.scope]) ?? []
  return [service.name, service.provider, service.description, service.status, service.verification.label, service.verification.evidence, ...operations]
    .join(" ")
    .toLocaleLowerCase("ko-KR")
    .includes(query)
}

function summaryCard(index, label, value, caption, tone) {
  const card = createElement("article", "summary-card")
  if (tone) card.dataset.tone = tone
  const heading = createElement("div", "summary-label")
  heading.append(createElement("span", "", label), createElement("span", "summary-index", `0${index}`))
  card.append(heading, createElement("strong", "summary-value", compactNumber(value)), createElement("small", "summary-caption", caption))
  return card
}

function renderSummary() {
  clear(elements.summary)
  elements.summary.setAttribute("aria-busy", "false")
  const awsResources = state.data.services
    .filter((service) => service.provider === "AWS")
    .reduce((sum, service) => sum + serviceResourceCount(service), 0)
  const gcpResources = state.data.services
    .filter((service) => service.provider === "GCP")
    .reduce((sum, service) => sum + serviceResourceCount(service), 0)
  elements.summary.append(
    summaryCard(1, "AWS 서비스", state.data.summary.awsServiceCount, `${awsResources}개 로컬 리소스`, "AWS"),
    summaryCard(2, "GCP 서비스", state.data.summary.gcpServiceCount, `${gcpResources}개 로컬 리소스`, "GCP"),
    summaryCard(3, "공식 SDK 검증", state.data.summary.sdkVerifiedCount, "실제 클라이언트 회귀 테스트", "SDK"),
    summaryCard(4, "HTTP 계약 검증", state.data.summary.contractVerifiedCount, "명시된 HTTP 경로 테스트", "CONTRACT"),
  )
}

function servicesForProvider() {
  if (!state.data) return []
  if (state.activeProvider === "ALL") return state.data.services
  return state.data.services.filter((service) => service.provider === state.activeProvider)
}

function renderProviderFilter() {
  clear(elements.providerFilter)
  for (const provider of ["ALL", "AWS", "GCP"]) {
    const count = provider === "ALL" ? state.data.services.length : state.data.services.filter((service) => service.provider === provider).length
    const button = createElement("button", "provider-filter-button")
    button.type = "button"
    button.dataset.provider = provider
    button.setAttribute("aria-pressed", String(state.activeProvider === provider))
    button.setAttribute("aria-label", provider === "ALL" ? `전체 제공자 ${count}개 서비스` : `${provider} ${count}개 서비스`)
    button.append(createElement("span", "", provider), createElement("small", "", String(count)))
    button.addEventListener("click", () => selectProvider(provider))
    elements.providerFilter.append(button)
  }
}

function navButton(id, name, count, provider = "ALL") {
  const button = createElement("button", "service-link")
  button.type = "button"
  button.dataset.service = id
  button.dataset.provider = provider
  if (state.activeService === id) button.setAttribute("aria-current", "page")
  button.append(
    createElement("span", "service-icon", serviceMarks[id] ?? "•"),
    createElement("span", "service-name", name),
    createElement("span", "service-count", String(count)),
  )
  button.addEventListener("click", () => selectService(id))
  return button
}

function renderNav() {
  clear(elements.nav)
  const services = servicesForProvider()
  const total = services.reduce((sum, service) => sum + serviceResourceCount(service), 0)
  const overviewName = state.activeProvider === "ALL" ? "전체 서비스" : `${state.activeProvider} 서비스`
  elements.nav.append(navButton("overview", overviewName, total, state.activeProvider))
  for (const service of services) {
    elements.nav.append(navButton(service.id, service.name, serviceResourceCount(service), service.provider))
  }
  elements.serviceTotal.textContent = state.activeProvider === "ALL" ? String(services.length) : `${services.length}/${state.data.services.length}`
}

function serviceCard(service) {
  const card = createElement("button", "service-card")
  card.type = "button"
  card.dataset.provider = service.provider
  card.addEventListener("click", () => selectService(service.id))

  const identity = createElement("div", "service-card-identity")
  const copy = createElement("div", "service-card-copy")
  copy.append(createElement("h3", "", service.name), createElement("p", "", service.description))
  identity.append(createElement("span", "service-icon", serviceMarks[service.id] ?? "•"), copy)

  const provider = createElement("span", "service-provider", service.provider)
  provider.dataset.provider = service.provider

  const verification = createElement("div", "service-verification")
  verification.dataset.level = service.verification.level
  verification.append(createElement("strong", "", service.verification.label), createElement("small", "", service.verification.evidence))

  const status = createElement("span", "status-chip", service.status)
  const resourceTotal = createElement("span", "resource-total", `${serviceResourceCount(service)} resources`)
  card.append(identity, provider, verification, status, resourceTotal)
  return card
}

function renderVerification(service) {
  clear(elements.verification)
  if (!service) {
    elements.verification.dataset.level = "OVERVIEW"
    const summary = createElement("div", "verification-summary")
    summary.append(
      createElement("strong", "", "검증 기준"),
      createElement("span", "", "READY는 실행 상태, 검증 배지는 실제 SDK 또는 HTTP 계약 회귀 테스트를 뜻합니다."),
    )
    elements.verification.append(summary)
    return
  }
  elements.verification.dataset.level = service.verification.level
  const summary = createElement("div", "verification-summary")
  summary.append(createElement("strong", "", service.verification.label), createElement("span", "", service.verification.evidence))
  elements.verification.append(summary)

  const operations = service.verification.operations ?? []
  if (!operations.length) return
  const details = createElement("details", "verification-details")
  details.open = state.openVerification.has(service.id)
  details.addEventListener("toggle", () => {
    if (details.open) state.openVerification.add(service.id)
    else state.openVerification.delete(service.id)
  })
  const toggle = createElement("summary", "", `검증 항목 ${operations.length}개 보기`)
  const list = createElement("div", "verification-operation-list")
  for (const operation of operations) {
    const row = createElement("div", "verification-operation")
    const identity = createElement("div")
    identity.append(createElement("strong", "", operation.name), createElement("p", "", operation.scope))
    const status = createElement("span", "verification-status", operation.status)
    status.dataset.status = operation.status
    row.append(identity, status)
    list.append(row)
  }
  details.append(toggle, list)
  const limitations = service.verification.limitations ?? []
  if (limitations.length) {
    const note = createElement("div", "verification-limitations")
    note.append(createElement("strong", "", "제외 범위"))
    const items = createElement("ul")
    for (const limitation of limitations) items.append(createElement("li", "", limitation))
    note.append(items)
    details.append(note)
  }
  details.append(createElement("small", "verification-source", `기준: ${service.verification.source}`))
  elements.verification.append(details)
}

function statusTone(status) {
  if (["EMPTY", "STORED", "CAPTURED"].includes(status)) return "muted"
  if (["DISABLED", "DESTROYED"].includes(status)) return "warning"
  return "positive"
}

function resourceActionConfigs(service, resource) {
  const actions = []
  if (service.id === "sqs" && resource.kind === "Queue") {
    actions.push({
      label: "비우기",
      title: `${shortName(resource.name)} 큐를 비울까요?`,
      description: "큐 설정은 유지하고 현재 대기 중인 메시지만 삭제합니다.",
      confirmLabel: "메시지 비우기",
      request: { operation: "purge", service: "sqs", resource: resource.name },
    })
  }
  if (service.id === "pubsub" && resource.kind === "Subscription") {
    actions.push({
      label: "비우기",
      title: `${shortName(resource.name)} 구독을 비울까요?`,
      description: "토픽과 구독 설정은 유지하고 현재 미확인 메시지만 삭제합니다.",
      confirmLabel: "메시지 비우기",
      request: { operation: "purge", service: "pubsub", resource: resource.name },
    })
  }
  if (service.id === "dynamodb" && resource.kind === "Table") {
    actions.push({
      label: "비우기",
      title: `${shortName(resource.name)} 테이블을 비울까요?`,
      description: "테이블 스키마는 유지하고 현재 저장된 아이템만 모두 삭제합니다.",
      confirmLabel: "아이템 비우기",
      request: { operation: "purge", service: "dynamodb", resource: resource.name },
    })
  }
  const deleteConfig = deleteResourceActionConfig(service, resource)
  if (deleteConfig) actions.push(deleteConfig)
  return actions
}

function deleteResourceActionConfig(service, resource) {
  const name = shortName(resource.name)
  if (service.id === "s3" && resource.kind === "Bucket") {
    return {
      label: "삭제",
      tone: "danger",
      title: `${name} 버킷을 삭제할까요?`,
      description: "버킷이 비어 있을 때만 삭제됩니다. 객체가 남아 있으면 요청을 거절합니다.",
      confirmLabel: "버킷 삭제",
      request: { operation: "delete", service: "s3", kind: "bucket", resource: resource.name },
    }
  }
  if (service.id === "sqs" && resource.kind === "Queue") {
    return {
      label: "삭제",
      tone: "danger",
      title: `${name} 큐를 삭제할까요?`,
      description: "큐 설정, 대기 메시지와 FIFO dedup 기록이 모두 삭제됩니다.",
      confirmLabel: "큐 삭제",
      request: { operation: "delete", service: "sqs", kind: "queue", resource: resource.name },
    }
  }
  if (service.id === "gcs" && resource.kind === "Bucket") {
    return {
      label: "삭제",
      tone: "danger",
      title: `${name} 버킷을 삭제할까요?`,
      description: "버킷이 비어 있을 때만 삭제됩니다. 객체가 남아 있으면 요청을 거절합니다.",
      confirmLabel: "버킷 삭제",
      request: { operation: "delete", service: "gcs", kind: "bucket", resource: resource.name },
    }
  }
  if (service.id === "dynamodb" && resource.kind === "Table") {
    return {
      label: "삭제",
      tone: "danger",
      title: `${name} 테이블을 삭제할까요?`,
      description: "테이블 스키마와 모든 아이템이 함께 삭제됩니다.",
      confirmLabel: "테이블 삭제",
      request: { operation: "delete", service: "dynamodb", kind: "table", resource: resource.name },
    }
  }
  if (service.id === "pubsub" && resource.kind === "Topic") {
    return {
      label: "삭제",
      tone: "danger",
      title: `${name} 토픽을 삭제할까요?`,
      description: "연결된 구독은 유지되지만 토픽 경로가 삭제된 토픽 상태로 전환됩니다.",
      confirmLabel: "토픽 삭제",
      request: { operation: "delete", service: "pubsub", kind: "topic", resource: resource.name },
    }
  }
  if (service.id === "pubsub" && resource.kind === "Subscription") {
    return {
      label: "삭제",
      tone: "danger",
      title: `${name} 구독을 삭제할까요?`,
      description: "구독 설정과 현재 미확인 메시지가 모두 삭제됩니다. 토픽은 유지됩니다.",
      confirmLabel: "구독 삭제",
      request: { operation: "delete", service: "pubsub", kind: "subscription", resource: resource.name },
    }
  }
  return null
}

function resourceRow(service, resource) {
  const row = createElement("article", "resource-row")
  const identity = createElement("div", "resource-identity")
  const resourceTitle = createElement("div", "resource-title")
  const title = createElement("strong", "", shortName(resource.name))
  title.title = resource.name
  const subtitleParts = [resource.kind]
  const timestamp = formatDate(resource.updatedAt || resource.createdAt)
  if (timestamp) subtitleParts.push(timestamp)
  const subtitle = createElement("small", "", subtitleParts.join(" · "))
  subtitle.title = resource.name
  resourceTitle.append(title, subtitle)
  identity.append(createElement("span", "resource-kind-icon", serviceMarks[service.id] ?? "•"), resourceTitle)

  const status = createElement("span", "resource-status", resource.status)
  status.dataset.tone = statusTone(resource.status)
  row.append(identity, status)

  const attributes = createElement("dl", "attribute-list")
  for (const attribute of resource.attributes ?? []) {
    const group = createElement("div")
    let value = attribute.value
    if (["저장 용량", "직렬화 크기", "요청 크기"].includes(attribute.label)) value = formatBytes(value)
    const term = createElement("dt", "", attribute.label)
    const description = createElement("dd", "", value)
    description.title = value
    group.append(term, description)
    attributes.append(group)
  }
  if (!attributes.childElementCount) {
    const group = createElement("div")
    group.append(createElement("dt", "", "경로"), createElement("dd", "", resource.name))
    attributes.append(group)
  }
  row.append(attributes)

  const actionConfigs = resourceActionConfigs(service, resource)
  if (actionConfigs.length) {
    row.classList.add("has-action")
    const actions = createElement("div", "resource-actions")
    for (const config of actionConfigs) {
      const action = createElement("button", "resource-action", config.label)
      action.type = "button"
      action.dataset.actionService = service.id
      if (config.tone) action.dataset.tone = config.tone
      action.setAttribute("aria-label", `${shortName(resource.name)} ${config.label}`)
      action.addEventListener("click", () => openConfirmation(config, action))
      actions.append(action)
    }
    row.append(actions)
  }
  return row
}

function emptyState(mark, title, description) {
  const wrapper = createElement("div", "empty-state")
  const content = createElement("div")
  content.append(createElement("div", "empty-state-mark", mark), createElement("h3", "", title), createElement("p", "", description))
  wrapper.append(content)
  return wrapper
}

function renderOverview() {
  renderServiceActions(null)
  renderVerification(null)
  const allServices = servicesForProvider()
  const services = allServices.filter(serviceMatchesQuery)
  const resourceCount = allServices.reduce((sum, service) => sum + serviceResourceCount(service), 0)
  elements.kicker.textContent = state.activeProvider === "ALL" ? "ALL CLOUDS" : state.activeProvider
  elements.title.textContent = state.query ? "서비스 검색 결과" : "서비스 상태"
  const providerDescription = state.activeProvider === "ALL" ? "AWS와 GCP" : state.activeProvider
  elements.description.textContent = state.query ? `“${state.query}”와 일치하는 ${providerDescription} 서비스입니다.` : `${providerDescription} 호환 서비스와 검증 근거를 확인합니다.`

  elements.status.textContent = state.query
    ? `${allServices.length}개 중 ${services.length}개 서비스를 찾았습니다.`
    : `${services.length}개 서비스 · ${resourceCount}개 리소스 · 전체 ${state.data.summary.messageCount}개 대기/캡처`
  if (!services.length) {
    elements.content.append(emptyState("?", "검색 결과가 없습니다", "서비스 이름, 제공자 또는 검증 API를 다른 단어로 검색해보세요."))
    return
  }
  const providers = state.activeProvider === "ALL" ? ["AWS", "GCP"] : [state.activeProvider]
  for (const provider of providers) {
    const providerServices = services.filter((service) => service.provider === provider)
    if (!providerServices.length) continue
    const section = createElement("section", "provider-section")
    section.dataset.provider = provider
    const heading = createElement("div", "provider-section-heading")
    heading.append(
      createElement("h3", "", provider === "AWS" ? "Amazon Web Services 호환" : "Google Cloud 호환"),
      createElement("span", "", `${providerServices.length} services`),
    )
    const grid = createElement("div", "service-grid")
    for (const service of providerServices) grid.append(serviceCard(service))
    section.append(heading, grid)
    elements.content.append(section)
  }
}

function renderService(service) {
  renderServiceActions(service)
  renderVerification(service)
  const page = state.page.service === service.id ? state.page : { resources: [], total: 0, offset: 0, limit: 25, hasPrevious: false, hasNext: false }
  const resources = page.resources
  elements.kicker.textContent = service.provider
  elements.title.textContent = service.name
  elements.description.textContent = service.description
  if (state.detailLoading && state.page.service === service.id) {
    elements.status.textContent = "리소스를 불러오는 중입니다."
    elements.content.append(resourceLoadingState())
    return
  }
  if (state.detailError && state.page.service === service.id) {
    elements.status.textContent = "리소스를 불러오지 못했습니다."
    const error = emptyState("!", "리소스 조회 오류", state.detailError)
    const retry = createElement("button", "inline-retry", "다시 시도")
    retry.type = "button"
    retry.addEventListener("click", () => loadServicePage(service.id, page.offset))
    error.querySelector("div")?.append(retry)
    elements.content.append(error)
    return
  }
  elements.status.textContent = state.query
    ? `${serviceResourceCount(service)}개 중 ${page.total}개 리소스가 검색됐습니다.`
    : `${serviceResourceCount(service)}개 리소스 · ${service.status}`

  if (!resources.length) {
    const searching = Boolean(state.query)
    elements.content.append(
      emptyState(
        serviceMarks[service.id] ?? "—",
        searching ? "검색 결과가 없습니다" : "현재 저장된 리소스가 없습니다",
        searching ? "이 서비스에서 다른 검색어를 입력해보세요." : "API는 준비 상태입니다. 애플리케이션이 리소스를 만들면 여기에 표시됩니다.",
      ),
    )
    return
  }
  const list = createElement("div", "resource-list")
  for (const resource of resources) list.append(resourceRow(service, resource))
  elements.content.append(list, paginationControls(service, page))
}

function resourceLoadingState() {
  const list = createElement("div", "resource-list resource-list-loading")
  for (let index = 0; index < 3; index += 1) list.append(createElement("div", "resource-row resource-row-placeholder"))
  return list
}

function paginationControls(service, page) {
  const controls = createElement("nav", "pagination", "")
  controls.setAttribute("aria-label", `${service.name} 리소스 페이지`)
  const start = page.total === 0 ? 0 : page.offset + 1
  const end = Math.min(page.offset + page.resources.length, page.total)
  const previous = createElement("button", "pagination-button", "이전")
  previous.type = "button"
  previous.disabled = !page.hasPrevious || state.detailLoading
  previous.addEventListener("click", () => loadServicePage(service.id, Math.max(0, page.offset - page.limit)))
  const current = createElement("span", "pagination-current", `${start}–${end} / ${page.total}`)
  const next = createElement("button", "pagination-button", "다음")
  next.type = "button"
  next.disabled = !page.hasNext || state.detailLoading
  next.addEventListener("click", () => loadServicePage(service.id, page.offset + page.limit))
  controls.append(previous, current, next)
  return controls
}

function renderContent() {
  clear(elements.content)
  clear(elements.serviceActions)
  if (!state.data) return
  if (state.activeService === "overview") {
    renderOverview()
    return
  }
  const service = state.data.services.find((candidate) => candidate.id === state.activeService)
  if (!service || (state.activeProvider !== "ALL" && service.provider !== state.activeProvider)) {
    state.activeService = "overview"
    renderOverview()
    return
  }
  renderService(service)
}

function selectService(id) {
  state.activeService = id
  renderNav()
  if (id === "overview") {
    state.page = { service: "", resources: [], total: 0, offset: 0, limit: 25, hasPrevious: false, hasNext: false, query: "" }
    renderContent()
  } else {
    state.page = { service: id, resources: [], total: 0, offset: 0, limit: 25, hasPrevious: false, hasNext: false, query: state.query }
    state.detailError = ""
    state.detailLoading = true
    renderContent()
    loadServicePage(id, 0)
  }
  if (window.innerWidth < 821) document.querySelector(".resource-panel")?.scrollIntoView({ behavior: "smooth", block: "start" })
}

function selectProvider(provider) {
  if (!state.data || !["ALL", "AWS", "GCP"].includes(provider)) return
  state.activeProvider = provider
  const active = state.data.services.find((service) => service.id === state.activeService)
  if (active && provider !== "ALL" && active.provider !== provider) state.activeService = "overview"
  renderProviderFilter()
  renderNav()
  renderContent()
}

function renderServiceActions(service) {
  clear(elements.serviceActions)
  if (!service) return
  const createConfig = creatableServices[service.id]
  if (createConfig) {
    const create = createElement("button", "service-action is-primary", "리소스 만들기")
    create.type = "button"
    create.dataset.actionService = service.id
    create.setAttribute("aria-label", `${service.name} 리소스 만들기`)
    create.addEventListener("click", () => openCreateDialog(service, create))
    elements.serviceActions.append(create)
  }
  if (service.id === "fcm" && serviceResourceCount(service) > 0) {
    const action = createElement("button", "service-action", "캡처 비우기")
    action.type = "button"
    action.dataset.actionService = "fcm"
    action.addEventListener("click", () =>
      openConfirmation(
        {
          title: "FCM 캡처를 비울까요?",
          description: "FCM 서비스 설정은 유지하고 지금까지 캡처된 요청 내역만 삭제합니다.",
          confirmLabel: "캡처 비우기",
          request: { operation: "purge", service: "fcm" },
        },
        action,
      ),
    )
    elements.serviceActions.append(action)
  }
  if (service.id === "vertex" && serviceResourceCount(service) > 0) {
    const action = createElement("button", "service-action", "호출 기록 비우기")
    action.type = "button"
    action.dataset.actionService = "vertex"
    action.addEventListener("click", () =>
      openConfirmation(
        {
          title: "Vertex AI 호출 기록을 비울까요?",
          description: "지금까지 캡처된 호출 메타데이터만 삭제합니다. 프롬프트와 생성 결과 본문은 원래 저장하지 않습니다.",
          confirmLabel: "호출 기록 비우기",
          request: { operation: "purge", service: "vertex" },
        },
        action,
      ),
    )
    elements.serviceActions.append(action)
  }
}

function formField(labelText, control, hint) {
  const field = createElement("label", "form-field")
  field.append(createElement("span", "form-label", labelText), control)
  if (hint) field.append(createElement("small", "form-hint", hint))
  return field
}

function textControl(name, placeholder, options = {}) {
  const input = createElement("input", "form-control")
  input.name = name
  input.type = options.type || "text"
  input.placeholder = placeholder
  input.required = options.required !== false
  input.autocomplete = "off"
  if (options.minLength) input.minLength = options.minLength
  if (options.maxLength) input.maxLength = options.maxLength
  if (options.min !== undefined) input.min = String(options.min)
  if (options.max !== undefined) input.max = String(options.max)
  if (options.value !== undefined) input.value = String(options.value)
  return input
}

function selectControl(name, choices) {
  const select = createElement("select", "form-control")
  select.name = name
  select.required = true
  for (const choice of choices) {
    const option = createElement("option", "", choice.label)
    option.value = choice.value
    if (choice.selected) option.selected = true
    select.append(option)
  }
  return select
}

function checkField(name, labelText, description) {
  const label = createElement("label", "check-field")
  const input = createElement("input")
  input.type = "checkbox"
  input.name = name
  const copy = createElement("span")
  copy.append(createElement("strong", "", labelText), createElement("small", "", description))
  label.append(input, copy)
  return { label, input }
}

function renderCreateFields(service) {
  clear(elements.createFields)
  elements.createError.hidden = true
  elements.createError.textContent = ""
  if (service.id === "s3") {
    const name = textControl("resource", "예: local-assets", { minLength: 3, maxLength: 63 })
    elements.createFields.append(formField("버킷 이름", name, "소문자, 숫자, 점과 하이픈을 사용할 수 있습니다."))
    return
  }
  if (service.id === "sqs") {
    const name = textControl("resource", "예: local-jobs", { maxLength: 80 })
    const queueType = selectControl("queueType", [
      { value: "standard", label: "Standard" },
      { value: "fifo", label: "FIFO" },
    ])
    const dedup = checkField("contentBasedDeduplication", "Content-based deduplication", "메시지 본문 SHA-256으로 5분 동안 중복을 제거합니다.")
    const hint = createElement("small", "form-hint", "FIFO를 선택하면 이름에 .fifo가 자동으로 붙습니다.")
    const syncQueueType = () => {
      const fifo = queueType.value === "fifo"
      dedup.input.disabled = !fifo
      if (!fifo) dedup.input.checked = false
      hint.textContent = fifo ? "만들 때 이름에 .fifo가 자동으로 붙습니다." : "Standard 큐는 FIFO 순서와 dedup을 적용하지 않습니다."
    }
    queueType.addEventListener("change", syncQueueType)
    elements.createFields.append(
      formField("큐 이름", name),
      formField("큐 유형", queueType),
      hint,
      dedup.label,
    )
    syncQueueType()
    return
  }
  if (service.id === "gcs") {
    const name = textControl("resource", "예: local-assets", { minLength: 3, maxLength: 63 })
    const location = selectControl("location", [
      { value: "asia-northeast3", label: "asia-northeast3 (Seoul)" },
      { value: "ASIA", label: "ASIA" },
      { value: "US", label: "US" },
      { value: "EU", label: "EU" },
    ])
    const storageClass = selectControl("storageClass", [
      { value: "STANDARD", label: "STANDARD" },
      { value: "NEARLINE", label: "NEARLINE" },
      { value: "COLDLINE", label: "COLDLINE" },
      { value: "ARCHIVE", label: "ARCHIVE" },
    ])
    elements.createFields.append(
      formField("버킷 이름", name, "소문자, 숫자, 점, 하이픈과 밑줄을 사용할 수 있습니다."),
      formField("리전", location),
      formField("Storage class", storageClass),
    )
    return
  }
  if (service.id === "dynamodb") {
    const name = textControl("resource", "예: notifications", { minLength: 3, maxLength: 255 })
    const partitionKey = textControl("partitionKey", "pk", { maxLength: 255, value: "pk" })
    const sortKey = textControl("sortKey", "선택 사항 (예: sk)", { maxLength: 255, required: false })
    elements.createFields.append(
      formField("테이블 이름", name, "문자, 숫자, 점, 하이픈과 밑줄을 사용할 수 있습니다."),
      formField("Partition key", partitionKey, "String 타입으로 생성합니다."),
      formField("Sort key", sortKey, "비워두면 partition key만 사용합니다."),
    )
    return
  }
  if (service.id === "pubsub") {
    const kind = selectControl("kind", [
      { value: "topic", label: "Topic" },
      { value: "subscription", label: "Subscription" },
    ])
    const name = textControl("resource", "예: local-events", { minLength: 3, maxLength: 255 })
    const extra = createElement("div", "pubsub-extra")
    const renderExtra = () => {
      clear(extra)
      if (kind.value !== "subscription") return
      const topics = state.page.service === "pubsub" ? state.page.resources.filter((resource) => resource.kind === "Topic") : []
      const topic = selectControl(
        "topic",
        topics.length
          ? topics.map((resource) => ({ value: resource.name, label: shortName(resource.name) }))
          : [{ value: "", label: "먼저 Topic을 만들어야 합니다" }],
      )
      const deadline = textControl("ackDeadlineSeconds", "10", { type: "number", min: 10, max: 600, value: 10 })
      const ordering = checkField("enableOrdering", "Message ordering", "Ordering key가 있는 메시지의 순서를 유지합니다.")
      extra.append(formField("연결 Topic", topic), formField("Ack deadline (초)", deadline), ordering.label)
    }
    kind.addEventListener("change", renderExtra)
    elements.createFields.append(formField("리소스 종류", kind), formField("이름", name), extra)
    renderExtra()
  }
}

function openCreateDialog(service, trigger) {
  if (state.actioning || !creatableServices[service.id]) return
  state.createService = service
  state.createTrigger = trigger ?? document.activeElement
  const config = creatableServices[service.id]
  elements.createTitle.textContent = `${config.label} 만들기`
  elements.createDescription.textContent = "로컬 data directory에 영속화되며 공식 SDK에서 바로 사용할 수 있습니다."
  renderCreateFields(service)
  elements.createDialog.showModal()
  elements.createFields.querySelector("input, select")?.focus()
}

function closeCreateDialog() {
  if (elements.createDialog.open) elements.createDialog.close()
}

function createRequestFromForm() {
  const service = state.createService
  if (!service) return null
  const data = new FormData(elements.createForm)
  let resource = String(data.get("resource") || "").trim()
  const parameters = {}
  let kind = creatableServices[service.id].kind
  if (service.id === "sqs") {
    parameters.queueType = String(data.get("queueType") || "standard")
    parameters.contentBasedDeduplication = data.has("contentBasedDeduplication") ? "true" : "false"
    if (parameters.queueType === "fifo" && !resource.endsWith(".fifo")) resource += ".fifo"
  }
  if (service.id === "gcs") {
    parameters.location = String(data.get("location") || "asia-northeast3")
    parameters.storageClass = String(data.get("storageClass") || "STANDARD")
  }
  if (service.id === "dynamodb") {
    parameters.partitionKey = String(data.get("partitionKey") || "pk").trim()
    parameters.sortKey = String(data.get("sortKey") || "").trim()
  }
  if (service.id === "pubsub") {
    kind = String(data.get("kind") || "topic")
    if (kind === "subscription") {
      parameters.topic = String(data.get("topic") || "")
      parameters.ackDeadlineSeconds = String(data.get("ackDeadlineSeconds") || "10")
      parameters.enableOrdering = data.has("enableOrdering") ? "true" : "false"
    }
  }
  return { operation: "create", service: service.id, kind, resource, parameters }
}

async function executeCreate(event) {
  event.preventDefault()
  if (state.actioning || !elements.createForm.reportValidity()) return
  const request = createRequestFromForm()
  if (!request) return
  state.actioning = true
  elements.createSubmit.disabled = true
  elements.createCancel.disabled = true
  elements.resetWorkload.disabled = true
  elements.createForm.setAttribute("aria-busy", "true")
  elements.createError.hidden = true
  try {
    const response = await fetch("/_fcp/actions", {
      method: "POST",
      headers: { Accept: "application/json", "Content-Type": "application/json" },
      body: JSON.stringify(request),
    })
    const result = await response.json().catch(() => ({}))
    if (!response.ok) throw new Error(result.message || `HTTP ${response.status}`)
    closeCreateDialog()
    await loadDashboard()
    showToast(result.message || "리소스를 만들었습니다.")
  } catch (error) {
    elements.createError.textContent = error instanceof Error ? error.message : "리소스를 만들지 못했습니다."
    elements.createError.hidden = false
    elements.createError.focus()
  } finally {
    state.actioning = false
    elements.createSubmit.disabled = false
    elements.createCancel.disabled = false
    elements.resetWorkload.disabled = false
    elements.createForm.setAttribute("aria-busy", "false")
  }
}

function openConfirmation(action, trigger) {
  if (state.actioning) return
  state.pendingAction = action
  state.actionTrigger = trigger ?? document.activeElement
  elements.dialogTitle.textContent = action.title
  elements.dialogDescription.textContent = action.description
  elements.dialogConfirm.textContent = action.confirmLabel
  elements.dialog.showModal()
  elements.dialogCancel.focus()
}

function closeConfirmation() {
  if (elements.dialog.open) elements.dialog.close()
}

function showToast(message, isError = false) {
  if (state.toastTimer) window.clearTimeout(state.toastTimer)
  elements.toast.textContent = message
  elements.toast.classList.toggle("is-error", isError)
  elements.toast.classList.add("is-visible")
  state.toastTimer = window.setTimeout(() => elements.toast.classList.remove("is-visible"), 3200)
}

async function executePendingAction() {
  if (!state.pendingAction || state.actioning) return
  const action = state.pendingAction
  state.actioning = true
  elements.dialogConfirm.disabled = true
  elements.resetWorkload.disabled = true
  try {
    const response = await fetch("/_fcp/actions", {
      method: "POST",
      headers: { Accept: "application/json", "Content-Type": "application/json" },
      body: JSON.stringify(action.request),
    })
    const result = await response.json().catch(() => ({}))
    if (!response.ok) throw new Error(result.message || `HTTP ${response.status}`)
    closeConfirmation()
    await loadDashboard()
    showToast(result.message || "작업을 완료했습니다.")
  } catch (error) {
    closeConfirmation()
    showToast(error instanceof Error ? error.message : "관리 작업을 완료하지 못했습니다.", true)
  } finally {
    state.actioning = false
    state.pendingAction = null
    elements.dialogConfirm.disabled = false
    elements.resetWorkload.disabled = false
  }
}

function renderError(message) {
  clear(elements.content)
  clear(elements.serviceActions)
  clear(elements.verification)
  elements.status.textContent = "FCP 상태를 불러오지 못했습니다."
  const wrapper = createElement("div", "error-state")
  const content = createElement("div")
  content.append(createElement("h3", "", "대시보드 연결 오류"), createElement("p", "", message))
  const retry = createElement("button", "", "다시 시도")
  retry.type = "button"
  retry.addEventListener("click", loadDashboard)
  content.append(retry)
  wrapper.append(content)
  elements.content.append(wrapper)
}

function updateConnectionState() {
  const age = state.lastSuccessAt ? Date.now() - state.lastSuccessAt : Number.POSITIVE_INFINITY
  let status = "connecting"
  let label = "연결 중"
  if (state.lastSuccessAt && age <= STALE_AFTER_MS) {
    status = state.failures ? "reconnecting" : "connected"
    label = state.failures ? "재연결 중" : "LOCAL"
  } else if (state.lastSuccessAt) {
    status = "stale"
    label = "STALE"
  } else if (state.failures) {
    status = "offline"
    label = "OFFLINE"
  }
  elements.connection.dataset.state = status
  elements.connectionLabel.textContent = label
  elements.connection.title = state.lastSuccessAt ? `마지막 성공: ${formatDate(new Date(state.lastSuccessAt).toISOString())}` : label
}

function markRequestSuccess(generatedAt) {
  const generated = new Date(generatedAt).getTime()
  state.lastSuccessAt = Number.isFinite(generated) ? generated : Date.now()
  state.failures = 0
  updateConnectionState()
}

function scheduleRefresh() {
  if (state.refreshTimer) window.clearTimeout(state.refreshTimer)
  state.refreshTimer = window.setTimeout(() => {
    if (document.hidden || state.actioning || elements.dialog.open || elements.createDialog.open) {
      scheduleRefresh()
      return
    }
    loadDashboard({ silent: true })
  }, AUTO_REFRESH_MS)
}

async function loadServicePage(serviceID, offset = 0, options = {}) {
  if (!state.data || serviceID === "overview") return
  const requestID = ++state.requestSerial
  const silent = Boolean(options.silent)
  if (!silent) {
    state.detailLoading = true
    state.detailError = ""
    if (state.page.service !== serviceID) {
      state.page = { service: serviceID, resources: [], total: 0, offset: 0, limit: 25, hasPrevious: false, hasNext: false, query: state.query }
    }
    renderContent()
  }
  const parameters = new URLSearchParams({
    view: "service",
    service: serviceID,
    limit: String(state.page.limit || 25),
    offset: String(Math.max(0, offset)),
  })
  if (state.query.trim()) parameters.set("q", state.query.trim())
  try {
    const response = await fetch(`/_fcp/dashboard?${parameters}`, { cache: "no-store", headers: { Accept: "application/json" } })
    if (!response.ok) throw new Error(`HTTP ${response.status}`)
    const detail = await response.json()
    if (requestID !== state.requestSerial || state.activeService !== serviceID) return
    const service = detail.services.find((candidate) => candidate.id === serviceID)
    const current = state.data.services.find((candidate) => candidate.id === serviceID)
    if (!service || !current || !detail.page) throw new Error("페이지 응답 형식이 올바르지 않습니다.")
    current.resourceCount = service.resourceCount
    current.status = service.status
    current.verification = service.verification
    state.page = { ...detail.page, resources: service.resources ?? [] }
    state.detailError = ""
    markRequestSuccess(detail.generatedAt)
  } catch (error) {
    if (requestID !== state.requestSerial || state.activeService !== serviceID) return
    state.failures += 1
    if (!silent) state.detailError = error instanceof Error ? error.message : "알 수 없는 오류가 발생했습니다."
    updateConnectionState()
  } finally {
    if (requestID === state.requestSerial && state.activeService === serviceID) {
      state.detailLoading = false
      renderNav()
      renderContent()
    }
  }
}

async function loadDashboard(options = {}) {
  if (state.loading) return
  const silent = Boolean(options.silent)
  state.loading = true
  if (!silent) {
    elements.refresh.disabled = true
    elements.refresh.classList.add("is-loading")
    elements.status.textContent = "최신 상태를 불러오는 중입니다."
  }
  try {
    const response = await fetch("/_fcp/dashboard?view=summary", { cache: "no-store", headers: { Accept: "application/json" } })
    if (!response.ok) throw new Error(`HTTP ${response.status}`)
    state.data = await response.json()
    markRequestSuccess(state.data.generatedAt)
    elements.project.textContent = state.data.project
    elements.project.title = state.data.project
    elements.updatedAt.textContent = `${formatDate(state.data.generatedAt)} 기준 · 3초 자동 갱신`
    renderSummary()
    renderProviderFilter()
    renderNav()
    renderContent()
    if (state.activeService !== "overview") await loadServicePage(state.activeService, state.page.offset, { silent: true })
  } catch (error) {
    state.failures += 1
    updateConnectionState()
    if (!state.data) renderError(error instanceof Error ? error.message : "알 수 없는 오류가 발생했습니다.")
  } finally {
    state.loading = false
    if (!silent) {
      elements.refresh.disabled = false
      elements.refresh.classList.remove("is-loading")
    }
    scheduleRefresh()
  }
}

elements.refresh.addEventListener("click", () => loadDashboard())
elements.resetWorkload.addEventListener("click", () =>
  openConfirmation(
    {
      title: "테스트 데이터를 모두 비울까요?",
      description: "버킷·큐·토픽·구독·Secret·KMS·IAM 구조와 로컬 키는 유지합니다. 객체, 메시지, Firestore 문서, FCM 캡처와 Vertex AI 호출 기록만 삭제합니다.",
      confirmLabel: "테스트 데이터 비우기",
      request: { operation: "reset-workload" },
    },
    elements.resetWorkload,
  ),
)
elements.dialogCancel.addEventListener("click", closeConfirmation)
elements.dialogConfirm.addEventListener("click", executePendingAction)
elements.dialog.addEventListener("close", () => {
  const trigger = state.actionTrigger
  state.actionTrigger = null
  if (!state.actioning) state.pendingAction = null
  if (trigger instanceof HTMLElement && trigger.isConnected) trigger.focus()
})
elements.createForm.addEventListener("submit", executeCreate)
elements.createCancel.addEventListener("click", closeCreateDialog)
elements.createDialog.addEventListener("close", () => {
  const trigger = state.createTrigger
  state.createTrigger = null
  state.createService = null
  if (trigger instanceof HTMLElement && trigger.isConnected) trigger.focus()
})
elements.search.addEventListener("input", (event) => {
  state.query = event.target.value
  if (state.searchTimer) window.clearTimeout(state.searchTimer)
  if (state.activeService === "overview") {
    renderContent()
    return
  }
  state.searchTimer = window.setTimeout(() => loadServicePage(state.activeService, 0), 250)
})
document.addEventListener("keydown", (event) => {
  const target = event.target
  const editing = target instanceof HTMLInputElement || target instanceof HTMLSelectElement || target instanceof HTMLTextAreaElement
  if (event.key === "/" && !editing && !elements.dialog.open && !elements.createDialog.open) {
    event.preventDefault()
    elements.search.focus()
  }
  if (event.key === "Escape" && document.activeElement === elements.search) {
    elements.search.value = ""
    state.query = ""
    elements.search.blur()
    if (state.activeService === "overview") renderContent()
    else loadServicePage(state.activeService, 0)
  }
})

document.addEventListener("visibilitychange", () => {
  if (document.hidden) {
    if (state.refreshTimer) window.clearTimeout(state.refreshTimer)
    return
  }
  loadDashboard({ silent: true })
})

window.setInterval(updateConnectionState, 1000)
updateConnectionState()
loadDashboard()
