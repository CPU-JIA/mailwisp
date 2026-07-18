package message

// ValidInboxLocalPart reports whether localPart can be created and accepted by
// MailWisp's canonical LMTP ingress without normalization ambiguity.
func ValidInboxLocalPart(localPart string) bool {
	if len(localPart) < 1 || len(localPart) > 64 {
		return false
	}
	for index, character := range localPart {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '.' && character != '_' && character != '-' {
			return false
		}
		if (index == 0 || index == len(localPart)-1) && (character == '.' || character == '_' || character == '-') {
			return false
		}
	}
	for index := 1; index < len(localPart); index++ {
		if localPart[index-1] == '.' && localPart[index] == '.' {
			return false
		}
	}
	return true
}
