# rgeocache

Модуль реверс-геокодинга с прегенирируемым кешем.

Кеш строится на основе карт OpenStreetMaps в формате pbf,
скачать их можно с <http://download.geofabrik.de/>

## Использование CLI

Основные варианты использования реализованные в cmd/main.go как cli,
список возможных параметров можно получить вызвав его без пераметров.

Примеры использования:

* ### Генерация кеша

```bash
go run cmd/main.go generate --input russia.osm.pbf --input ./europe/belarus.osm.pbf --points cis_points
```

где russia_points - название файла кеша (будет сохранен с постфиксом .gob)  
russia.osm.pbf и ./europe/belarus.osm.pbf - входные файлы  

Генерация кеша росcии занимет около ~50Гб оперативки. Есть возможнозность пренести нагрузку из памяти на диск указав параметр --cache /tmp/rgeo_cache (в качестве пути можно указать любую директорию), в этом случае процесс геренерации может значительно замедлится

* ### HTTP Api

```bash
go run cmd/main.go serve --points cis_points
```

Запускает http сервер с простым api для реверс-геокодинга на основе заданного кеша.  
Документация к api описана в формате openapi в файле docs/api.yaml  
Пример простейшего запроса:

```bash
curl -X GET 'localhost:8080/rgeocode/address/59.9176846/30.3930866'
{"name":"","street":"набережная Обводного канала","house_number":"5 литА","city":"Санкт-Петербург"}
```

## Использование как go модуля

Для go программ можно избежать http прослойки и использовать геокодер напрямую  
Для этого неообходимо подулючить модуль github.com/royalcat/rgeocache/geocoder
Пример использования:

```go
geocache := &geocoder.RGeoCoder{}
err := geocache.LoadFromPointsFile("cis_points.gob")
loc, ok := rgeocoder.Find(lat, lon)
fmt.Printf("%s %s %s", loc.City, loc.Street, loc.HouseNumber)
```

## Скрипты для удобства

В generate_scripts есть скрипты для автоматического скачивания карт и генерации кеша.  
Скрипты разделены по регионам

* post-cis - страны постсоветского пространства и бывшего снг
* russia
