// Команда makeicon записывает иконку приложения в файл .ico — он нужен
// установщику Windows (Inno Setup) для значка самого setup.exe, ярлыков и
// записи в «Программы и компоненты».
//
// Иконка рисуется процедурно в internal/server, поэтому бинарных ресурсов
// в репозитории нет, а установщик, ярлыки и область уведомлений используют
// одно и то же изображение.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/zigorminsk-debug/my-mikrotik/internal/server"
)

func main() {
	out := flag.String("out", "windows/app.ico", "путь к создаваемому файлу .ico")
	flag.Parse()
	data := server.IconICO()
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		log.Fatalf("не удалось записать %s: %v", *out, err)
	}
	fmt.Printf("%s: %d байт\n", *out, len(data))
}
