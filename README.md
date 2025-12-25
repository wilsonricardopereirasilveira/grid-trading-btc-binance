```
rm -f grid-bot; go build -o grid-bot cmd/main.go && chmod +x grid-bot && nohup ./grid-bot > /dev/null 2>&1 & sleep 1; tail -F logs/app.log

go build -o grid-bot cmd/main.go
chmod +x grid-bot
nohup ./grid-bot > /dev/null 2>&1 &
tail -F logs/app.log
```