<div align="center">
  <img src="design/logo/avatar.svg" width="112" alt="Логотип OpenFlux">
  <h1>OpenFlux</h1>
  <p>Зашифрованный многоклиентный VPN-туннель через Yandex Volga для Android, компьютера и выходной Linux-ноды.</p>
  <p>
    <a href="https://github.com/DaemonSet/OpenFlux/releases/latest"><img src="https://img.shields.io/github/v/release/DaemonSet/OpenFlux?display_name=tag&amp;sort=semver&amp;style=flat-square&amp;color=7aa2f7" alt="Последний релиз"></a>
    <a href="https://github.com/DaemonSet/OpenFlux/actions/workflows/ci.yml"><img src="https://github.com/DaemonSet/OpenFlux/actions/workflows/ci.yml/badge.svg" alt="Статус CI"></a>
    <a href="LICENSE"><img src="https://img.shields.io/github/license/DaemonSet/OpenFlux?style=flat-square" alt="Лицензия GPL-3.0"></a>
    <img src="https://img.shields.io/badge/Android-8.0%2B-3DDC84?style=flat-square&amp;logo=android&amp;logoColor=white" alt="Android 8 или новее">
  </p>
  <p>
    <img src="https://img.shields.io/badge/Go-1.26.4%2B-00ADD8?style=flat-square&amp;logo=go&amp;logoColor=white" alt="Go 1.26.4 или новее">
    <img src="https://img.shields.io/badge/Java-17-ED8B00?style=flat-square&amp;logo=openjdk&amp;logoColor=white" alt="Java 17">
    <img src="https://img.shields.io/badge/ABI-ARM64%20%7C%20ARMv7%20%7C%20x86__64%20%7C%20x86-455a64?style=flat-square" alt="Поддерживаемые архитектуры Android">
    <img src="https://img.shields.io/badge/IPv4%20%2F%20TCP-experimental-f59e0b?style=flat-square" alt="Экспериментальная поддержка IPv4 и TCP">
  </p>
  <p><a href="README.md">English</a> · <strong>Русский</strong></p>
</div>

![OpenFlux Android: подключение, логи и настройки](docs/images/openflux-android-tabs.png)

> Это экспериментальный независимо развиваемый форк
> [p1neappleXpress/OpenFlux](https://github.com/p1neappleXpress/OpenFlux).
> Основные отличия от исходного проекта перечислены в [FORK.md](FORK.md).

OpenFlux — экспериментальный зашифрованный VPN-туннель, использующий Yandex
Volga как транспорт. Основное направление этого форка — Android VPN-клиенты
и Linux exit-нода с multi-client маршрутизацией, автоматическим восстановлением
Volga-сессии и сквозным шифрованием.

**[Скачать последнюю Android-версию](https://github.com/DaemonSet/OpenFlux/releases/latest)**

```text
Android VPN или SOCKS5-клиент -> зашифрованный Volga-транспорт -> Linux-нода -> интернет
```

## Возможности

- Android-клиент для Android 8+ на системном `VpnService` со сборками для ARM,
  ARM64, x86 и x86_64;
- интерфейс в стиле Android 11 с подключением, логами и настройками;
- аутентифицированное шифрование AES-256-GCM и получение ключа через scrypt;
- хранение ссылки и общего секрета с защитой Android Keystore;
- зашифрованная проверка задержки и живой график пинга;
- DNSMux через exit-ноду с фильтрацией AdGuard DNS и резервным DNS;
- SOCKS5-клиент для компьютера и режим выходной Linux-ноды;
- устойчивый Yandex Volga transport с reconnect и полным re-auth;
- одновременная работа нескольких Android-клиентов через одну exit-ноду.

## Важные ограничения

OpenFlux — экспериментальный исследовательский проект, а не проверенная замена
WireGuard или другому зрелому VPN. Android-туннель сейчас поддерживает IPv4 и
TCP. DNS передаётся через зашифрованный DNSMux; произвольный UDP и IPv6 через
туннель пока не передаются. Владелец транспорта по-прежнему видит метаданные: время
соединения, объём трафика и зашифрованные данные. Пользователь с правом
редактирования документа может нарушить доступность соединения.

Используйте программу только на своих системах и сетях либо там, где у вас есть
разрешение на тестирование.

## Требования

- Go 1.26.4 или новее для клиента компьютера и выходной ноды;
- Linux VPS/VDS с root-доступом для выходной ноды;
- для сборки Android: Java 17, Android SDK/API 35, Build Tools 35.0.0,
  NDK 27.0.12077973, Gradle 8.14.3 и `gomobile`;
- редактируемый документ в старом редакторе Yandex Docs при использовании
  транспорта Yandex.

## Подготовка приватной конфигурации

Создайте эти файлы локально и передайте те же значения на выходную ноду. Они
исключены через `.gitignore`, их нельзя добавлять в Git:

```bash
printf '%s\n' 'https://ссылка-на-ваш-документ' > document-url
openssl rand -base64 32 > encryption-key
chmod 600 document-url encryption-key
```

Секрет шифрования должен содержать не менее 16 символов. Используйте уникальное
случайное значение, а не обычный пароль. Если ссылка или секрет раскрыты,
замените оба значения.

## Сборка ноды и клиента компьютера

```bash
go build -o openflux .
```

Запустите выходную Linux-ноду от root:

```bash
sudo iptables -C OUTPUT -p tcp --tcp-flags RST RST -j DROP 2>/dev/null || \
  sudo iptables -I OUTPUT 1 -p tcp --tcp-flags RST RST -j DROP
sudo ./openflux --exit-node --transport yandex \
  --url-file ./document-url --encryption-key-file ./encryption-key
```

Пример [systemd-сервиса](deploy/openflux.service) ожидает бинарник и приватные
файлы в `/root/openflux`. Перед установкой проверьте пути:

```bash
sudo install -d -m 700 /root/openflux
sudo install -m 755 ./openflux /root/openflux/openflux
sudo install -m 600 ./document-url ./encryption-key /root/openflux/
sudo install -m 644 deploy/openflux.service /etc/systemd/system/openflux.service
sudo systemctl daemon-reload
sudo systemctl enable --now openflux
sudo systemctl status openflux
```

Запустите клиент компьютера и настройте в браузере SOCKS5-прокси
`127.0.0.1:1080`:

```bash
./openflux --client --transport yandex --socks5 127.0.0.1:1080 \
  --url-file ./document-url --encryption-key-file ./encryption-key
```

Добавляйте `--debug` только при диагностике и проверяйте логи перед публикацией.

## Сборка и установка Android-приложения

Укажите `ANDROID_SDK_ROOT` (или `ANDROID_HOME`), установите `gomobile` и Gradle,
затем выполните:

```bash
go install golang.org/x/mobile/cmd/gomobile@v0.0.0-20260908204917-8b95e45f8d3e
go install golang.org/x/mobile/cmd/gobind@v0.0.0-20260908204917-8b95e45f8d3e
gomobile init
./build_android_app.sh
```

Сборка создаёт отдельные APK для `arm64-v8a`, `armeabi-v7a`, `x86_64` и `x86`, а
также `OpenFlux-android-universal-debug.apk` для устройств с неизвестной
архитектурой. Передайте подходящий APK на устройство с Android 8+, установите,
укажите собственные ссылку и общий секрет во вкладке **«Настройки»**, затем
подтвердите системный запрос Android на создание VPN.

Настройки сохраняются после обычного обновления приложения, если Application ID
и сертификат подписи не менялись. Очистка данных или удаление приложения стирает
их. APK с другим сертификатом не сможет обновить установленную версию. Артефакты
CI подписаны debug-ключом, а APK в GitHub Releases — постоянным release-ключом.
При переходе с debug на release приложение потребуется один раз удалить, поэтому
сохранённые настройки будут очищены.

Дополнительные сведения находятся в [android/README.md](android/README.md).

## Флаги командной строки

| Флаг | По умолчанию | Описание |
| --- | --- | --- |
| `--client` | выкл. | Запустить SOCKS5-клиент |
| `--exit-node` | выкл. | Запустить выходную ноду (нужен root) |
| `--socks5` | `:1080` | Адрес SOCKS5-прокси |
| `--transport` | `yandex` | Транспорт (`yandex` или `oneme`) |
| `--url` | пусто | Ссылка в аргументе; безопаснее `--url-file` |
| `--url-file` | пусто | Прочитать ссылку на документ из файла |
| `--encryption-key-file` | пусто | Прочитать секрет транспорта Yandex из файла |
| `--maxToken` | пусто | Токен транспорта MAX |
| `--maxUid` | пусто | ID пользователя транспорта MAX |
| `--debug` | выкл. | Включить подробные логи |

## Разработка и безопасность

Перед коммитом выполните:

```bash
gofmt -w $(git ls-files '*.go')
go test ./...
go vet ./...
git diff --check
```

Правила участия находятся в [CONTRIBUTING.md](CONTRIBUTING.md), порядок сообщения
об уязвимостях — в [SECURITY.md](SECURITY.md), список изменений — в
[CHANGELOG.md](CHANGELOG.md).

## Лицензия

OpenFlux распространяется по GNU General Public License v3.0 или более поздней
версии. См. [LICENSE](LICENSE), [COPYRIGHT](COPYRIGHT) и [NOTICE](NOTICE). Этот
форк не одобрен Yandex и не связан с компанией.
