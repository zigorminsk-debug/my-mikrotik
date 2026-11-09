package com.mikrotik.inspector;

import org.junit.Test;
import static org.junit.Assert.*;

public class UpdateCheckerTest {
    @Test public void comparesNumericVersions() {
        assertTrue(UpdateChecker.isNewer("v1.10.0", "1.9.9"));
        assertFalse(UpdateChecker.isNewer("v1.9.9", "1.10.0"));
        assertFalse(UpdateChecker.isNewer("v1.2.3", "v1.2.3"));
        assertTrue(UpdateChecker.isNewer("v1.2.3", "1.2.3-dev.4"));
        assertFalse(UpdateChecker.isNewer("v1.2.3", "dev"));
    }
}
