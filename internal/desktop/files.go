package desktop

import "errors"

func (d *desktop) chooseFile(kind string) (string, error) {
	switch kind {
	case "import":
		return d.app.Dialog.OpenFile().SetTitle("导入记忆").AddFilter("JSON 记忆文件", "*.json").AttachToWindow(d.window).PromptForSingleSelection()
	case "share-import":
		return d.app.Dialog.OpenFile().SetTitle("导入分享材料").AddFilter("分享材料", "*.txt;*.md").AttachToWindow(d.window).PromptForSingleSelection()
	case "export":
		return d.app.Dialog.SaveFile().SetMessage("导出记忆").SetFilename("respire-memories.json").AddFilter("JSON 记忆文件", "*.json").AttachToWindow(d.window).PromptForSingleSelection()
	case "backup":
		return d.app.Dialog.SaveFile().SetMessage("备份数据库").SetFilename("respire-backup.db").AddFilter("数据库备份", "*.db").AttachToWindow(d.window).PromptForSingleSelection()
	case "keys":
		return d.app.Dialog.SaveFile().SetMessage("导出密钥").SetFilename("respire-keys.txt").AddFilter("密钥材料", "*.txt").AttachToWindow(d.window).PromptForSingleSelection()
	default:
		return "", errors.New("不支持的文件操作")
	}
}
