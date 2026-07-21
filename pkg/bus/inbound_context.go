package bus

import "strings"

// RawKeyWhatsAppAuthenticatedSenderE164 is the InboundContext.Raw key that
// carries the authenticated native WhatsApp sender's phone number in E.164 form
// (e.g. "+33695651381").
//
// It is a native-WhatsApp-channel-owned trusted marker: the native channel is
// the only writer, and it populates the key exclusively from whatsmeow-
// authenticated sender metadata — for both direct phone-JID senders and LID
// senders whose phone was resolved via SenderAlt or a LID->PN lookup. It is
// never derived from message content, tool arguments, or any other channel.
// Downstream consumers (the agent exec-context layer) treat its presence as the
// sole trusted signal of an authenticated native WhatsApp sender.
const RawKeyWhatsAppAuthenticatedSenderE164 = "whatsapp_authenticated_sender_e164"

// NormalizeInboundMessage ensures the inbound context is normalized and keeps
// convenience mirrors in sync for runtime consumers.
func NormalizeInboundMessage(msg InboundMessage) InboundMessage {
	if msg.Context.Channel == "" {
		msg.Context.Channel = msg.Channel
	}
	if msg.Context.ChatID == "" {
		msg.Context.ChatID = msg.ChatID
	}
	if msg.Context.SenderID == "" {
		msg.Context.SenderID = msg.SenderID
	}
	if msg.Context.MessageID == "" {
		msg.Context.MessageID = msg.MessageID
	}
	msg.Context = normalizeInboundContext(msg.Context)
	msg.Channel = msg.Context.Channel
	msg.SenderID = msg.Context.SenderID
	msg.ChatID = msg.Context.ChatID
	if msg.MessageID == "" {
		msg.MessageID = msg.Context.MessageID
	}
	if msg.Context.MessageID == "" {
		msg.Context.MessageID = msg.MessageID
	}
	return msg
}

func (ctx InboundContext) isZero() bool {
	return ctx.Channel == "" &&
		ctx.Account == "" &&
		ctx.ChatID == "" &&
		ctx.ChatType == "" &&
		ctx.TopicID == "" &&
		ctx.SpaceID == "" &&
		ctx.SpaceType == "" &&
		ctx.SenderID == "" &&
		ctx.MessageID == "" &&
		!ctx.Mentioned &&
		ctx.ReplyToMessageID == "" &&
		ctx.ReplyToSenderID == "" &&
		len(ctx.ReplyHandles) == 0 &&
		len(ctx.Raw) == 0
}

func normalizeInboundContext(ctx InboundContext) InboundContext {
	ctx.Channel = strings.TrimSpace(ctx.Channel)
	ctx.Account = strings.TrimSpace(ctx.Account)
	ctx.ChatID = strings.TrimSpace(ctx.ChatID)
	ctx.ChatType = normalizeKind(ctx.ChatType)
	ctx.TopicID = strings.TrimSpace(ctx.TopicID)
	ctx.SpaceID = strings.TrimSpace(ctx.SpaceID)
	ctx.SpaceType = normalizeKind(ctx.SpaceType)
	ctx.SenderID = strings.TrimSpace(ctx.SenderID)
	ctx.MessageID = strings.TrimSpace(ctx.MessageID)
	ctx.ReplyToMessageID = strings.TrimSpace(ctx.ReplyToMessageID)
	ctx.ReplyToSenderID = strings.TrimSpace(ctx.ReplyToSenderID)
	ctx.ReplyHandles = cloneStringMap(ctx.ReplyHandles)
	ctx.Raw = cloneStringMap(ctx.Raw)
	return ctx
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}

	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func normalizeKind(kind string) string {
	return strings.ToLower(strings.TrimSpace(kind))
}
