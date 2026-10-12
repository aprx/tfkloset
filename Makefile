
all: tfkloset


tfkloset:
	go build -o tfkloset .

clean:
	rm -f tfkloset

re: clean all

.PHONY: all clean re tfkloset
