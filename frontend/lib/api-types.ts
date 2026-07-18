/**
 * API response shapes for UpstreamOps backend.
 * Keep in sync with backend/storage/*.go and backend/api/*.go.
 */

export type ChannelType = "newapi" | "sub2api"

export type CredentialMode = "password" | "token"

export type RechargeMultiplierMode = "divide" | "multiply"

export type NotificationChannelType =
  | "telegram"
  | "webhook"
  | "email"
  | "wecom"
  | "dingtalk"
  | "feishu"
  | "serverchan3"

export type CaptchaProviderType =
  | "capsolver"
  | "2captcha"
  | "anticaptcha"
  | "yescaptcha"

export type MonitorJob = "login" | "balance" | "rates"

export type NotificationEvent =
  | "balance_low"
  | "rate_changed"
  | "rate_structure_changed"
  | "rate_added"
  | "rate_removed"
  | "announcement"
  | "login_failed"
  | "captcha_failed"
  | "monitor_failed"
  | "subscription_daily_remaining_low"
  | "subscription_weekly_remaining_low"
  | "subscription_monthly_remaining_low"
  | "subscription_expiring"
  | "upstream_sync_group_changed"
  | "sub2_pool_changed"

export interface Channel {
  id: number
  name: string
  type: ChannelType
  site_url: string
  username: string
  sort_order: number
  user_id?: string
  credential_mode: CredentialMode
  login_extra_params: string
  turnstile_enabled: boolean
  ignore_announcements: boolean
  subscription_enabled: boolean
  proxy_enabled: boolean
  captcha_config_id?: number | null
  balance_threshold: number
  recharge_multiplier?: number | null
  recharge_multiplier_mode: RechargeMultiplierMode
  monitor_enabled: boolean
  last_balance?: number | null
  last_balance_at?: string | null
  today_cost?: number | null
  total_cost?: number | null
  last_error?: string
  created_at: string
  updated_at: string
}

export interface ChannelPage {
  items: Channel[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface CaptchaConfig {
  id: number
  name: string
  type: CaptchaProviderType
  endpoint?: string
  extra?: string
  enabled: boolean
  proxy_enabled: boolean
  last_balance?: number | null
  balance_unit?: string
  balance_at?: string | null
  balance_error?: string
  created_at: string
  updated_at: string
}

export interface RateSnapshot {
  id: number
  channel_id: number
  remote_group_id?: number | null
  model_name: string
  description?: string
  ratio: number
  completion_ratio: number
  first_seen_at: string
  last_seen_at: string
}

export interface RateChangeLog {
  id: number
  channel_id: number
  model_name: string
  old_ratio: number | null
  new_ratio: number
  old_completion_ratio?: number | null
  new_completion_ratio?: number
  changed_at: string
}

export interface RateChangeLogPage {
  items: RateChangeLog[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface BalanceSnapshot {
  id: number
  channel_id: number
  balance: number
  sampled_at: string
}

export interface NotificationSubscription {
  channel_ids: number[]
  mode: "all" | "groups"
  groups?: string[]
  events?: NotificationEvent[]
}

export interface NotificationChannel {
  id: number
  name: string
  type: NotificationChannelType
  enabled: boolean
  proxy_enabled: boolean
  subscriptions?: string
  created_at: string
  updated_at: string
}

export interface NotificationLog {
  id: number
  channel_id: number
  upstream_channel_id?: number
  channel_name?: string
  channel_type?: string
  event: NotificationEvent
  subject: string
  body: string
  success: boolean
  error_message?: string
  sent_at: string
}

export interface UpstreamAnnouncement {
  id: number
  channel_id: number
  source_key: string
  title?: string
  content: string
  type?: string
  link?: string
  published_at?: string | null
  source_updated_at?: string | null
  first_seen_at: string
}

export interface MonitorLog {
  id: number
  channel_id: number
  job: MonitorJob
  success: boolean
  error_message?: string
  duration_ms: number
  started_at: string
  finished_at: string
}

export interface DashboardLowest {
  channel_id: number
  name: string
  balance: number | null
}

export interface DashboardChannelStat {
  id: number
  name: string
  type: string
  monitor_enabled: boolean
  last_balance?: number | null
  today_cost?: number | null
  total_cost?: number | null
  last_error?: string
}

export interface DashboardSummary {
  total_channels: number
  active_channels: number
  failed_channels: number
  total_balance: number
  today_total_cost: number
  total_cost: number
  lowest_balance: DashboardLowest | null
  channels: DashboardChannelStat[]
  recent_rate_changes: RateChangeLog[]
}

export interface BalanceTrendPoint {
  day: string
  balance: number
}

export interface CostTrendPoint {
  day: string
  cost: number
}

export interface SystemAuthConfig {
  enabled: boolean
  username: string
  password: string
  tokenSecret: string
  sessionTTLHours: number
}

export interface AppConfig {
  title: string
  notificationPrefix: string
}

export interface SystemSchedulerRetentionConfig {
  cron: string
  monitorLogsDays: number
  balanceSnapshotsDays: number
  notificationLogsDays: number
  announcementsDays: number
}

export interface SystemSchedulerConfig {
  balanceCron: string
  rateCron: string
  concurrency: number
  retention: SystemSchedulerRetentionConfig
}

export interface SystemNotificationsConfig {
  batchRateChanges: boolean
  minChangePct: number
  balanceLowCooldownMinutes: number
  subscriptionDailyRemainingThresholdPct: number
  subscriptionWeeklyRemainingThresholdPct: number
  subscriptionMonthlyRemainingThresholdPct: number
  subscriptionExpiryThresholdHours: number
  subscriptionAlertCooldownMinutes: number
  sendMaxAttempts: number
}

export interface SystemProxyConfig {
  enabled: boolean
  versionCheckEnabled: boolean
  protocol: "http" | "https" | "socks5"
  host: string
  port: number
  username: string
  password: string
}

export interface SystemUpstreamConfig {
  timeoutSeconds: number
  userAgent: string
}

export interface SystemConfig {
  app: AppConfig
  auth: SystemAuthConfig
  scheduler: SystemSchedulerConfig
  notifications: SystemNotificationsConfig
  proxy: SystemProxyConfig
  upstream: SystemUpstreamConfig
}

export interface SystemConfigResponse {
  config_path: string
  config: SystemConfig
}

export interface AppVersion {
  name: string
  title: string
  version: string
  latest_version?: string
  update_available?: boolean
  repo_url?: string
  release_url?: string
  update_error?: string
}

export interface ApplyConfigResult {
  applied_sections: string[]
  message: string
}

export interface Sub2PoolTarget {
  id: string
  name: string
  description?: string
  enabled?: boolean
  account_count?: number
  refreshed_at?: string | null
}

export interface Sub2PoolSnapshotSummary {
  total_accounts: number
  schedulable_accounts?: number
  healthy_accounts?: number
  debt_accounts?: number
  missing_multiplier_accounts?: number
  missing_data_accounts?: number
}

export interface Sub2PoolAccount {
  id: number
  name: string
  platform: string
  type: string
  business_channel?: string | null
  min_group?: string | null
  current_priority?: number | null
  suggested_priority?: number | null
  upstream_multiplier?: number | null
  balance?: number | null
  balance_status?: string | null
  health_status?: string | null
  rate_limit_status?: string | null
  schedulable?: boolean
  schedulable_reason?: string | null
  today_requests?: number | null
  current_concurrency?: number | null
  max_concurrency?: number | null
  missing_data?: string[]
  updated_at?: string | null
}

export interface Sub2PoolSnapshot {
  target_id: string
  target_name?: string
  refreshed_at?: string | null
  snapshot_signature?: string | null
  summary?: Sub2PoolSnapshotSummary
  accounts: Sub2PoolAccount[]
}

export interface Sub2PoolPriorityPreviewItem {
  account_id: number
  account_name: string
  before_priority?: number | null
  target_priority?: number | null
  skip_reason?: string | null
  multiplier_before?: number | null
  multiplier_target?: number | null
}

export interface Sub2PoolPriorityPreviewSummary {
  total: number
  changed: number
  skipped: number
}

export interface Sub2PoolGuardViolation {
  code: string
  message: string
  count?: number
}

export interface Sub2PoolPriorityPreviewResponse {
  target_id: string
  snapshot_signature: string
  snapshot_at?: string | null
  items: Sub2PoolPriorityPreviewItem[]
  summary?: Sub2PoolPriorityPreviewSummary
  guards?: Sub2PoolGuardViolation[]
}

export interface Sub2PoolPriorityApplySummary {
  priority_changes: number
  multiplier_changes: number
  skipped: number
  combined_result?: string
}

export interface Sub2PoolPriorityApplyConflict {
  expected_signature?: string
  actual_signature?: string
  message?: string
}

export interface Sub2PoolPriorityApplyResult {
  target_id: string
  snapshot_signature: string
  applied_at?: string | null
  message: string
  summary?: Sub2PoolPriorityApplySummary
  conflict?: Sub2PoolPriorityApplyConflict
  applied?: Array<{
    account_id: number
    account_name: string
    channel: string
    before_priority: number
    target_priority: number
    after_priority?: number | null
    status: string
  }>
  failed?: Array<{
    account_id: number
    account_name: string
    channel: string
    before_priority: number
    target_priority: number
    after_priority?: number | null
    status: string
  }>
}

export interface Sub2PoolSchedulableResult {
  account_id: number
  schedulable: boolean
  message?: string
  updated_at?: string | null
  account?: Partial<Sub2PoolAccount>
}

export interface Sub2PoolAutomationLastResult {
  at?: string | null
  success?: boolean
  summary?: string
  priority_changes?: number
  multiplier_changes?: number
  skipped?: number
  guard_blocked?: boolean
  guard_reason?: string
}

export interface Sub2PoolAutomationStatus {
  target_id: string
  enabled: boolean
  schedule?: string
  last_run_at?: string | null
  last_result?: Sub2PoolAutomationLastResult | null
  guard_blocked?: boolean
  guard_block_reasons?: string[]
  updated_at?: string | null
}

export interface Sub2PoolAutomationUpdateResult {
  enabled: boolean
  message?: string
  updated_at?: string | null
  status?: Sub2PoolAutomationStatus
}

export interface ChannelRedeemResult {
  message: string
  type: string
  value: number
  new_balance?: number
  new_concurrency?: number
  group_name?: string
  validity_days?: number
}

export type RechargePaymentMethod = "alipay" | "wxpay"
export type SubscriptionPaymentMethod =
  | "balance"
  | "alipay"
  | "wxpay"
  | "stripe"
  | "creem"
  | "waffo_pancake"
  | string

export interface ChannelRechargeMethod {
  type: RechargePaymentMethod
  name: string
  min_amount: number
  max_amount: number
}

export interface ChannelRechargeInfo {
  amount_label: string
  amount_step: number
  min_amount: number
  max_amount: number
  preset_amounts: number[]
  help_text?: string
  help_image_url?: string
  alipay_force_qrcode: boolean
  methods: ChannelRechargeMethod[]
}

export interface ChannelRechargeLaunch {
  mode: "qrcode" | "redirect" | "form" | "success"
  qr_code?: string
  pay_url?: string
  form_action?: string
  form_fields?: Record<string, string>
  expires_at?: string
}

export interface ChannelSubscriptionMethod {
  type: SubscriptionPaymentMethod
  name: string
}

export interface ChannelSubscriptionPlan {
  id: string
  name: string
  description?: string
  price: number
  currency?: string
  validity?: string
  group_name?: string
  quota?: number
  daily_limit_usd?: number | null
  weekly_limit_usd?: number | null
  monthly_limit_usd?: number | null
  features?: string[]
  payment_methods?: string[]
}

export interface ChannelSubscriptionInfo {
  plans: ChannelSubscriptionPlan[]
  methods: ChannelSubscriptionMethod[]
}

export type ChannelSubscriptionLaunch = ChannelRechargeLaunch

export interface ChannelSubscriptionUsageWindow {
  limit_usd: number
  used_usd: number
  remaining_usd: number
  remaining_percent: number
  used_percent: number
  window_start?: string | null
  resets_at?: string | null
  resets_in_seconds: number
}

export interface ChannelSubscriptionUsage {
  id: number
  group_id: number
  group_name: string
  status: string
  starts_at?: string | null
  expires_at?: string | null
  expires_in_days: number
  daily?: ChannelSubscriptionUsageWindow | null
  weekly?: ChannelSubscriptionUsageWindow | null
  monthly?: ChannelSubscriptionUsageWindow | null
}

export interface ChannelSubscriptionUsageInfo {
  items: ChannelSubscriptionUsage[]
}

export type ChannelAPIKeyStatus = "active" | "disabled" | "expired" | "quota_exhausted" | "unknown"

export interface ChannelAPIKey {
  id: number
  key: string
  name: string
  status: ChannelAPIKeyStatus | string
  group?: string
  group_name?: string
  group_description?: string
  group_ratio: number
  group_id?: number | null
  quota: number
  quota_used: number
  unlimited_quota: boolean
  expired_time: number
  expires_at?: string | null
  created_at?: string | null
  updated_at?: string | null
  last_used_at?: string | null
  allow_ips?: string
  ip_whitelist?: string[]
  ip_blacklist?: string[]
  model_limits_enabled: boolean
  model_limits?: string
  cross_group_retry: boolean
  rate_limit_5h: number
  rate_limit_1d: number
  rate_limit_7d: number
  usage_5h: number
  usage_1d: number
  usage_7d: number
}

export interface ChannelAPIKeyPage {
  items: ChannelAPIKey[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface NotificationLogPage {
  items: NotificationLog[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface UpstreamAnnouncementPage {
  items: UpstreamAnnouncement[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface ChannelAPIKeyGroup {
  id?: number | null
  name: string
  description?: string
  ratio: number
}

export interface ChannelAPIKeyReveal {
  key: string
}

export interface UpstreamSyncTarget {
  id: number
  name: string
  base_url: string
  enabled: boolean
  last_check_status?: string
  last_check_at?: string | null
  last_check_error?: string
}

export interface UpstreamSyncTargetGroup {
  id: number
  target_id: number
  remote_group_id: number
  name: string
  platform?: string
  ratio: number
  status: string
  sort: number
  description?: string
  last_sync_at?: string | null
}

export interface UpstreamSyncTargetProxy {
  id: number
  name: string
  protocol: string
  host: string
  port: number
  status: string
}

export type UpstreamSyncRateConvertMode = "raw" | "multiply_100" | "divide_100" | "custom"

export interface UpstreamSyncAccount {
  id?: number
  source_channel_id: number
  source_group_id?: number | null
  source_group_name?: string
  proxy_id?: number | null
  concurrency: number
  weight: number
  rate_convert_mode: UpstreamSyncRateConvertMode
  rate_convert_value: number
  enabled: boolean
  test_enabled: boolean
  test_model?: string
}

export interface UpstreamSyncGroup {
  id: number
  display_name: string
  name_template: string
  name: string
  target_id: number
  target_group_ids: number[]
  platform: string
  model_limits_mode: string
  model_limits?: string
  pool_mode_enabled: boolean
  pool_mode_retry_count: number
  pool_mode_retry_status_codes?: string
  custom_error_codes_enabled: boolean
  custom_error_codes?: string
  rate_sort_direction: "asc" | "desc"
  accounts: UpstreamSyncAccount[]
  enabled: boolean
  apply_status?: string
  apply_error?: string
  last_applied_at?: string | null
}

export interface UpstreamSyncLog {
  id: number
  sync_group_id: number
  target_id: number
  action: string
  success: boolean
  message?: string
  created_at: string
}

export interface UpstreamSyncLogPage {
  items: UpstreamSyncLog[]
  total: number
  page: number
  page_size: number
  pages: number
}

export type GroupDiscoveryCandidateStatus =
  | "pending"
  | "approved"
  | "rejected"
  | "applying"
  | "applied"
  | "failed"

export interface GroupDiscoveryCandidate {
  id: number
  source_channel_id: number
  source_channel_name: string
  source_group_id?: number | null
  source_group_name: string
  source_group_description?: string
  ratio: number
  status: GroupDiscoveryCandidateStatus
  target_id?: number | null
  target_group_ids: number[]
  target_group_names: string[]
  platform: string
  account_name: string
  concurrency: number
  weight: number
  source_api_key_id?: number | null
  source_api_key_name?: string
  target_account_id?: number | null
  target_account_name?: string
  apply_error?: string
  last_attempt_at?: string | null
  applied_at?: string | null
  discovered_at: string
  last_seen_at: string
}

export interface GroupDiscoveryScanError {
  channel_id: number
  channel_name: string
  error: string
}

export interface GroupDiscoveryScanResult {
  total_channels: number
  scanned_channels: number
  new_candidates: number
  updated_candidates: number
  errors?: GroupDiscoveryScanError[]
}

export interface GroupDiscoveryApplyItemResult {
  id: number
  status: GroupDiscoveryCandidateStatus
  error?: string
}

export interface GroupDiscoveryApplyResult {
  requested: number
  applied: number
  failed: number
  items: GroupDiscoveryApplyItemResult[]
}
