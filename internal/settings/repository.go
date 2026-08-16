package settings

// FileRepository 使用单个 YAML 文件保存配置，是文件持久化适配器。
type FileRepository struct {
	path string
}

func NewFileRepository(path string) *FileRepository {
	return &FileRepository{path: path}
}

func (r *FileRepository) Save(config *Config) error {
	return config.Save(r.path)
}
