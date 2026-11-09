package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"strings"
)

func (d *Discord) replyLogArchive(ctx context.Context, i discordInteraction) error {
	if !d.accept(i) {
		return nil
	}
	var date string
	for _, option := range i.Data.Options {
		if option.Name == "date" && json.Unmarshal(option.Value, &date) != nil {
			return d.reply(ctx, i, "Укажите дату в формате ГГГГ-ММ-ДД.")
		}
	}
	name, data, message := d.actions.ExportLogs(ctx, strings.TrimSpace(date))
	if message != "" {
		return d.reply(ctx, i, message)
	}

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("payload_json", `{"content":"Архив дневных логов. Храните его приватно.","attachments":[{"id":0}]}`); err != nil {
		return err
	}
	file, err := form.CreateFormFile("files[0]", name)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return err
	}
	endpoint := "/webhooks/" + i.ApplicationID + "/" + i.Token + "/messages/@original"
	if err := d.api.requestBody(ctx, "PATCH", endpoint, "reply", form.FormDataContentType(), body.Bytes(), nil); err != nil {
		return d.reply(ctx, i, "Discord не подтвердил отправку архива. Попробуйте ещё раз.")
	}
	return nil
}
