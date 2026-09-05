import unittest
from src.slugify import slugify


class SlugifyTests(unittest.TestCase):
    def test_basic_words(self):
        self.assertEqual(slugify('Hello, World!'), 'hello-world')

    def test_repeated_delimiters(self):
        self.assertEqual(slugify('  one___two  '), 'one-two')
