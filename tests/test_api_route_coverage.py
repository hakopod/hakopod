import importlib.util
from pathlib import Path
import unittest

MODULE = Path(__file__).resolve().parents[1] / 'api/check_route_coverage.py'
spec = importlib.util.spec_from_file_location('route_coverage', MODULE)
coverage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(coverage)


class RouteCoverageTests(unittest.TestCase):
    def test_literal_routes_ignore_comments_and_include_registration_tables(self):
        source = '''
        // "POST /api/v1/comment"
        /* "DELETE /api/v1/comment" */
        mux.HandleFunc("GET /api/v1/items", list)
        routes := []route{{"POST /api/v1/items", create}}
        mux.HandleFunc(method + "/api/v1/dynamic", handler)
        '''
        self.assertEqual(coverage.literal_routes(source), {
            ('GET', '/api/v1/items'), ('POST', '/api/v1/items')})

    def test_new_route_requires_contract_or_explained_exclusion(self):
        route = ('POST', '/api/v1/new-action')
        self.assertTrue(coverage.check_coverage({route}, set(), {}))
        self.assertEqual(coverage.check_coverage({route}, {route}, {}), [])
        self.assertEqual(coverage.check_coverage({route}, set(), {route: 'Internal protocol.'}), [])
        self.assertTrue(coverage.check_coverage({route}, set(), {route: ''}))

    def test_removed_or_documented_route_cannot_keep_exclusion(self):
        route = ('POST', '/api/v1/retired')
        exclusion = {route: 'Retired route.'}
        self.assertTrue(coverage.check_coverage(set(), set(), exclusion))
        self.assertTrue(coverage.check_coverage({route}, {route}, exclusion))


if __name__ == '__main__':
    unittest.main()
