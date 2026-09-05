import unittest
from src.slugify import slugify


class HiddenSlugifyTests(unittest.TestCase):
    def test_accents_and_boundaries(self):
        self.assertEqual(slugify('Crème brûlée!'), 'creme-brulee')
        self.assertEqual(slugify('---Already--clean---'), 'already-clean')

    def test_non_ascii_and_empty(self):
        self.assertEqual(slugify('東京 café'), 'cafe')
        self.assertEqual(slugify('___'), '')
