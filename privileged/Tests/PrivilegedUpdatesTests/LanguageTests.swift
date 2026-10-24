import XCTest
@testable import PrivilegedUpdates

final class LanguageTests: XCTestCase {
    func testExplicitPreferenceWinsOverSystemLanguage() {
        XCTAssertEqual(authorizationLanguage(preference: "en", preferredLanguages: ["zh-Hans"]), "en")
        XCTAssertEqual(authorizationLanguage(preference: "zh-Hans", preferredLanguages: ["en-AU"]), "zh-Hans")
    }
    func testSystemUsesPrimaryLanguageAndUnknownFallsBackToEnglish() {
        for language in ["zh", "zh-Hans-CN", "zh_CN", "zh-SG"] {
            XCTAssertEqual(authorizationLanguage(preference: "system", preferredLanguages: [language]), "zh-Hans")
        }
        for languages in [[], ["fr", "zh-Hans"], ["zh-Hant"], ["en-AU"]] {
            XCTAssertEqual(authorizationLanguage(preference: nil, preferredLanguages: languages), "en")
        }
        XCTAssertEqual(authorizationLanguage(preference: "unknown", preferredLanguages: ["zh-Hans"]), "en")
    }
}
