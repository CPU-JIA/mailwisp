export interface Inbox {
  id: string
  address: string
  status: 'active'
  expires_at: string
  created_at: string
}
export interface Capability {
  token: string
  kid: string
  scopes: string[]
  expires_at: string
}

export interface CreatedInbox {
  inbox: Inbox
  capability: Capability
}

export interface BrowserSession {
	inbox: Inbox
	expires_at: string
	csrf_token: string
}

export interface MailAddress {
  name: string
  address: string
}

export interface Attachment {
  part_path: string
  file_name: string
  content_type: string
  disposition: string
  content_id: string
  size_bytes: number
}

export interface ParseWarning {
  code: string
  part_path: string
  detail: string
}

export interface MessageSummary {
  id: string
  envelope_sender: string
  subject: string
  preview: string
  received_at: string
  parse_status: 'pending' | 'processing' | 'parsed' | 'failed'
  size_bytes: number
  has_attachments: boolean
}

export interface MessageDetail extends MessageSummary {
  header_message_id: string
  from: MailAddress[]
  to: MailAddress[]
  cc: MailAddress[]
  sent_at: string | null
  text: string
  html_source: string
  attachments: Attachment[]
  warnings: ParseWarning[]
}

export interface ErrorEnvelope {
  error: {
    code: string
    message: string
    request_id: string
  }
}
