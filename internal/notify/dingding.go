package notify

type DingDingNotifier struct {
	Webhook string
	Secret  string
}

func (d *DingDingNotifier) Name() string { return "dingding" }

func (d *DingDingNotifier) Send(title, message string) error {
	payload := map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"title": title,
			"text":  message,
		},
	}
	return postJSON(d.Webhook, payload)
}
