"""Count the words in a file."""

import argparse
import re


def words(text):
    return re.findall(r"[a-z']+", text.lower())


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("file")
    args = p.parse_args()
    with open(args.file) as f:
        print(f"{len(words(f.read()))} words")


if __name__ == "__main__":
    main()
