package notify

type WeChatNotifier struct {
	Webhook string
}

func (w *WeChatNotifier) Name() string { return "wechat" }

func (w *WeChatNotifier) Send(title, message string) error {
	payload := map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"content": message,
		},
	}
	return postJSON(w.Webhook, payload)
}
