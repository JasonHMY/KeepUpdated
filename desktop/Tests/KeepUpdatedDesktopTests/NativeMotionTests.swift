import XCTest
@testable import KeepUpdatedDesktop

final class NativeMotionTests: XCTestCase {
    func testHandoffDoesNotExposeTwoReadablePages() {
        // Complementary page progress must not make both bodies readable mid-handoff.
        for step in 0...100 {
            let progress = Double(step) / 100
            XCTAssertLessThan(min(NativeMotion.opacity(progress), NativeMotion.opacity(1 - progress)), 0.025)
        }
    }

    func testVisibilityIsBoundedAndMonotonic() {
        XCTAssertEqual(NativeMotion.opacity(-0.1), 0)
        XCTAssertEqual(NativeMotion.opacity(0), 0)
        XCTAssertEqual(NativeMotion.opacity(1), 1)
        XCTAssertEqual(NativeMotion.opacity(1.1), 1)
        var previous = 0.0
        for step in 0...100 {
            let opacity = NativeMotion.opacity(Double(step) / 100)
            XCTAssertGreaterThanOrEqual(opacity, previous)
            XCTAssertLessThanOrEqual(opacity, 1)
            previous = opacity
        }
    }
}
