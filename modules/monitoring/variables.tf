# ---- Standard inputs (docs/04-module-registry.md section 3.1) ----

variable "name" {
  description = "Logical name. The alarm topic and dashboard are named <team>-<environment>-<name>."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,40}$", var.name))
    error_message = "name must be 3-41 characters of lowercase letters, digits and hyphens, starting with a letter."
  }
}

variable "team" {
  description = "Owning team slug. Injected by the platform."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,30}$", var.team))
    error_message = "team must be 2-31 characters of lowercase letters, digits and hyphens, starting with a letter."
  }
}

variable "environment" {
  description = "Target environment. Injected by the platform."
  type        = string

  validation {
    condition     = contains(["development", "staging", "production"], var.environment)
    error_message = "environment must be one of development, staging, production."
  }
}

variable "stack" {
  description = "Name of the stack that owns this instance. Injected by the platform and used for the iacp:stack tag."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,62}$", var.stack))
    error_message = "stack must be 2-63 characters of lowercase letters, digits and hyphens, starting with a letter."
  }
}

variable "tags" {
  description = "Additional tags. Keys starting with 'iacp:' are reserved for platform tags."
  type        = map(string)
  default     = {}

  validation {
    condition     = length(var.tags) <= 30 && alltrue([for k in keys(var.tags) : !startswith(k, "iacp:")])
    error_message = "At most 30 tags are allowed and the 'iacp:' key prefix is reserved."
  }
}

# ---- Alarm topic ----

variable "kms_key_arn" {
  description = "Existing KMS key ARN for the alarm topic. Its policy must let CloudWatch (and every publisher_service_principals entry) use it. If null, the module creates a dedicated key with rotation enabled."
  type        = string
  default     = null

  validation {
    condition     = var.kms_key_arn == null || can(regex("^arn:aws[a-z-]*:kms:[a-z0-9-]+:[0-9]{12}:key/.+$", var.kms_key_arn))
    error_message = "kms_key_arn must be a KMS key ARN (aliases are not accepted; the AWS-managed alias/aws/sns key cannot be granted to CloudWatch)."
  }
}

variable "publisher_service_principals" {
  description = "AWS service principals, besides cloudwatch.amazonaws.com, allowed to publish to the topic and use its key for this account (for example events.amazonaws.com)."
  type        = list(string)
  default     = []
  nullable    = false

  validation {
    condition     = length(var.publisher_service_principals) <= 10 && alltrue([for p in var.publisher_service_principals : can(regex("^[a-z0-9-]+(\\.[a-z0-9-]+)*\\.amazonaws\\.com$", p))])
    error_message = "publisher_service_principals must be at most 10 AWS service principals such as events.amazonaws.com."
  }
}

variable "subscriptions" {
  description = <<-EOT
    Topic subscriptions keyed by a stable name (1-40 lowercase letters, digits and hyphens).
    protocol: email, email-json, https, sqs or lambda (plain http is not allowed).
    endpoint: an email address, an https:// URL, or an SQS queue / Lambda function ARN to match the protocol.
    raw_message_delivery: deliver the raw message (https and sqs only).
    Email subscriptions stay pending until the recipient confirms them.
  EOT
  type = map(object({
    protocol             = string
    endpoint             = string
    raw_message_delivery = optional(bool, false)
  }))
  default  = {}
  nullable = false

  validation {
    condition     = length(var.subscriptions) <= 20 && alltrue([for k in keys(var.subscriptions) : can(regex("^[a-z0-9][a-z0-9-]{0,39}$", k))])
    error_message = "At most 20 subscriptions, keyed by 1-40 lowercase letters, digits and hyphens."
  }

  validation {
    condition = alltrue([
      for s in values(var.subscriptions) : try(
        s.protocol == "email" || s.protocol == "email-json" ? can(regex("^[^@\\s]+@[^@\\s]+\\.[^@\\s]+$", s.endpoint)) :
        s.protocol == "https" ? can(regex("^https://[^\\s]+$", s.endpoint)) :
        s.protocol == "sqs" ? can(regex("^arn:aws[a-z-]*:sqs:[a-z0-9-]+:[0-9]{12}:.+$", s.endpoint)) :
        s.protocol == "lambda" ? can(regex("^arn:aws[a-z-]*:lambda:[a-z0-9-]+:[0-9]{12}:function:.+$", s.endpoint)) :
        false,
        false,
      )
    ])
    error_message = "Each subscription needs protocol email, email-json, https, sqs or lambda, with a matching endpoint (email address, https:// URL, SQS queue ARN or Lambda function ARN)."
  }

  validation {
    condition     = alltrue([for s in values(var.subscriptions) : !s.raw_message_delivery || contains(["https", "sqs"], s.protocol)])
    error_message = "raw_message_delivery is only supported for https and sqs subscriptions."
  }
}

# ---- Dashboard ----

variable "dashboard_widgets" {
  description = <<-EOT
    Dashboard widgets, in order (at most 100). A widget is either a metric graph (metrics: a list of
    { namespace, metric_name, dimensions, stat, label }) or a text panel (markdown), never both.
    view: timeSeries (default), singleValue, bar or pie. period: 60 or a multiple of 60 seconds (default 300).
    width: 1-24 grid columns (default 12). height: 1-24 rows (default 6).
    An alarm status widget for every alarm this module creates is appended automatically.
    The dashboard is created when there is at least one widget or alarm.
  EOT
  type = list(object({
    title = string
    metrics = optional(list(object({
      namespace   = string
      metric_name = string
      dimensions  = optional(map(string), {})
      stat        = optional(string, "Average")
      label       = optional(string)
    })), [])
    markdown = optional(string)
    view     = optional(string, "timeSeries")
    stacked  = optional(bool, false)
    period   = optional(number, 300)
    width    = optional(number, 12)
    height   = optional(number, 6)
  }))
  default  = []
  nullable = false

  validation {
    condition = length(var.dashboard_widgets) <= 100 && alltrue([
      for w in var.dashboard_widgets :
      length(w.title) >= 1 && length(w.title) <= 256 && (w.markdown == null) == (length(w.metrics) > 0) && length(w.metrics) <= 50
    ])
    error_message = "At most 100 widgets; each needs a 1-256 character title and either 1-50 metrics or markdown, not both."
  }

  validation {
    condition = alltrue([
      for w in var.dashboard_widgets :
      contains(["timeSeries", "singleValue", "bar", "pie"], w.view) &&
      w.period >= 60 && w.period % 60 == 0 &&
      w.width >= 1 && w.width <= 24 && floor(w.width) == w.width &&
      w.height >= 1 && w.height <= 24 && floor(w.height) == w.height
    ])
    error_message = "view must be timeSeries, singleValue, bar or pie; period a multiple of 60 seconds; width 1-24; height 1-24."
  }

  validation {
    condition = alltrue(flatten([
      for w in var.dashboard_widgets : [
        for m in w.metrics :
        can(regex("^[A-Za-z0-9/_.#:-]{1,255}$", m.namespace)) &&
        can(regex("^.{1,255}$", m.metric_name)) &&
        length(m.dimensions) <= 30 &&
        can(regex("^(SampleCount|Average|Sum|Minimum|Maximum|p[0-9]{1,2}(\\.[0-9]{1,2})?|p100)$", m.stat))
      ]
    ]))
    error_message = "Each metric needs a namespace and metric_name of 1-255 characters, at most 30 dimensions, and stat SampleCount, Average, Sum, Minimum, Maximum or a percentile such as p99."
  }
}

# ---- Alarm helpers ----

variable "sqs_queue_alarms" {
  description = <<-EOT
    SQS queues to alarm on, keyed by a stable name (1-40 lowercase letters, digits and hyphens).
    oldest_message_age_seconds: alarm when the oldest message is older than this (default 900, at least 60).
    visible_messages_threshold: optional alarm when more messages than this are waiting.
  EOT
  type = map(object({
    queue_name                 = string
    oldest_message_age_seconds = optional(number, 900)
    visible_messages_threshold = optional(number)
  }))
  default  = {}
  nullable = false

  validation {
    condition = length(var.sqs_queue_alarms) <= 25 && alltrue([
      for k, a in var.sqs_queue_alarms :
      can(regex("^[a-z0-9][a-z0-9-]{0,39}$", k)) &&
      can(regex("^[A-Za-z0-9_-]{1,80}$", trimsuffix(a.queue_name, ".fifo"))) &&
      a.oldest_message_age_seconds >= 60 &&
      (a.visible_messages_threshold == null || try(a.visible_messages_threshold >= 0, false))
    ])
    error_message = "At most 25 queues keyed by 1-40 lowercase letters, digits and hyphens; queue_name must be an SQS queue name, oldest_message_age_seconds at least 60 and visible_messages_threshold at least 0."
  }
}

variable "lambda_function_alarms" {
  description = <<-EOT
    Lambda functions to alarm on, keyed by a stable name (1-40 lowercase letters, digits and hyphens).
    errors_threshold / throttles_threshold: alarm when the 5-minute sum reaches this many errors / throttles (default 1).
  EOT
  type = map(object({
    function_name       = string
    errors_threshold    = optional(number, 1)
    throttles_threshold = optional(number, 1)
  }))
  default  = {}
  nullable = false

  validation {
    condition = length(var.lambda_function_alarms) <= 25 && alltrue([
      for k, a in var.lambda_function_alarms :
      can(regex("^[a-z0-9][a-z0-9-]{0,39}$", k)) &&
      can(regex("^[A-Za-z0-9_-]{1,64}$", a.function_name)) &&
      a.errors_threshold >= 1 && a.throttles_threshold >= 1
    ])
    error_message = "At most 25 functions keyed by 1-40 lowercase letters, digits and hyphens; function_name must be a Lambda function name and both thresholds at least 1."
  }
}

variable "dynamodb_table_alarms" {
  description = <<-EOT
    DynamoDB tables to alarm on, keyed by a stable name (1-40 lowercase letters, digits and hyphens).
    throttle_events_threshold: alarm when the 5-minute sum of read or of write throttle events reaches this (default 1).
  EOT
  type = map(object({
    table_name                = string
    throttle_events_threshold = optional(number, 1)
  }))
  default  = {}
  nullable = false

  validation {
    condition = length(var.dynamodb_table_alarms) <= 25 && alltrue([
      for k, a in var.dynamodb_table_alarms :
      can(regex("^[a-z0-9][a-z0-9-]{0,39}$", k)) &&
      can(regex("^[A-Za-z0-9_.-]{3,255}$", a.table_name)) &&
      a.throttle_events_threshold >= 1
    ])
    error_message = "At most 25 tables keyed by 1-40 lowercase letters, digits and hyphens; table_name must be a DynamoDB table name and throttle_events_threshold at least 1."
  }
}
