/**
 * Statsig JavaScript Implementation Test File
 * 
 * This file demonstrates ALL Statsig features for testing the migration skill:
 * - Feature Gates
 * - Dynamic Configs
 * - Experiments
 * - Session Replay
 * - Autocapture/Web Analytics
 * - Custom Events
 * - Complex User Context
 */

import { StatsigClient } from '@statsig/js-client';
import { runStatsigSessionReplay } from '@statsig/session-replay';
import { runStatsigAutoCapture } from '@statsig/web-analytics';

// ============================================
// 1. USER CONTEXT SETUP
// ============================================
const statsigUser = {
  userID: 'user-abc-123',
  email: 'john.doe@example.com',
  ip: '192.168.1.1',
  userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)',
  country: 'US',
  locale: 'en-US',
  appVersion: '3.2.1',
  custom: {
    accountType: 'premium',
    company: 'Acme Corp',
    role: 'admin',
    teamSize: 50,
    subscriptionTier: 'enterprise',
    betaTester: true
  },
  customIDs: {
    organizationID: 'org-456',
    workspaceID: 'ws-789',
    teamID: 'team-101'
  },
  privateAttributes: {
    creditCardLast4: '1234',
    phoneNumber: '+1-555-0123',
    internalEmployeeID: 'emp-567'
  }
};

// ============================================
// 2. INITIALIZE STATSIG CLIENT
// ============================================
const statsigClient = new StatsigClient(
  'client-sdk-key-here',
  statsigUser,
  {
    environment: {
      tier: 'production'
    },
    loggingIntervalMs: 10000,
    disableCurrentPageLogging: false
  }
);

// Initialize session replay and autocapture BEFORE client initialization
runStatsigSessionReplay(statsigClient);
runStatsigAutoCapture(statsigClient);

// Initialize the client
async function initializeStatsig() {
  try {
    await statsigClient.initializeAsync();
    console.log('Statsig initialized successfully');
  } catch (error) {
    console.error('Failed to initialize Statsig:', error);
  }
}

// ============================================
// 3. FEATURE GATES
// ============================================
function checkFeatureGates() {
  // Simple feature gate
  if (statsigClient.checkGate('new_dashboard_experience')) {
    console.log('New dashboard enabled');
    renderNewDashboard();
  } else {
    console.log('Using legacy dashboard');
    renderLegacyDashboard();
  }

  // Feature gate with underscore naming
  const hasPremiumFeatures = statsigClient.checkGate('premium_features_enabled');
  if (hasPremiumFeatures) {
    enablePremiumUI();
  }

  // Feature gate with hyphen naming (will need conversion)
  const betaAccess = statsigClient.checkGate('beta-features-access');
  
  // Feature gate without exposure logging
  const silentCheck = statsigClient.checkGate('silent_feature_check', {
    disableExposureLog: true
  });

  // Multiple related gates
  const canExportData = statsigClient.checkGate('export_data_enabled');
  const canBulkEdit = statsigClient.checkGate('bulk_edit_enabled');
  const canUseAPI = statsigClient.checkGate('api_access_enabled');
}

// ============================================
// 4. DYNAMIC CONFIGS
// ============================================
function getDynamicConfigs() {
  // Homepage configuration
  const homepageConfig = statsigClient.getConfig('homepage_configuration');
  const heroTitle = homepageConfig.get('hero_title', 'Welcome to Our Platform');
  const heroSubtitle = homepageConfig.get('hero_subtitle', 'Get started today');
  const showPromotion = homepageConfig.get('show_promotion_banner', false);
  const promotionDiscount = homepageConfig.get('promotion_discount', 0.1);
  const featuredProducts = homepageConfig.get('featured_products', []);

  // Pricing tiers configuration
  const pricingConfig = statsigClient.getConfig('pricing_tiers');
  const tiers = pricingConfig.get('tiers', {
    basic: { price: 9.99, features: ['feature1'] },
    pro: { price: 29.99, features: ['feature1', 'feature2'] },
    enterprise: { price: 99.99, features: ['feature1', 'feature2', 'feature3'] }
  });
  const currency = pricingConfig.get('currency', 'USD');
  const discountCode = pricingConfig.get('discount_code', null);

  // UI theme configuration
  const themeConfig = statsigClient.getConfig('ui_theme_config');
  const primaryColor = themeConfig.get('primary_color', '#007bff');
  const darkModeDefault = themeConfig.get('dark_mode_default', false);
  const fontFamily = themeConfig.get('font_family', 'Inter, sans-serif');

  // Feature limits configuration
  const limitsConfig = statsigClient.getConfig('feature_limits');
  const maxUploads = limitsConfig.get('max_uploads_per_day', 10);
  const maxFileSize = limitsConfig.get('max_file_size_mb', 50);
  const maxTeamMembers = limitsConfig.get('max_team_members', 5);

  // Config without exposure
  const silentConfig = statsigClient.getConfig('silent_config', {
    disableExposureLog: true
  });
}

// ============================================
// 5. EXPERIMENTS
// ============================================
function runExperiments() {
  // Checkout flow experiment
  const checkoutExperiment = statsigClient.getExperiment('checkout_flow_optimization');
  const checkoutLayout = checkoutExperiment.get('layout', 'single_page');
  const showProgressBar = checkoutExperiment.get('show_progress_bar', true);
  const expressCheckoutEnabled = checkoutExperiment.get('express_checkout', false);
  const paymentMethods = checkoutExperiment.get('payment_methods', ['card', 'paypal']);

  // Onboarding experiment
  const onboardingExperiment = statsigClient.getExperiment('onboarding_flow_test');
  const onboardingSteps = onboardingExperiment.get('num_steps', 3);
  const showTutorial = onboardingExperiment.get('show_tutorial', true);
  const tutorialType = onboardingExperiment.get('tutorial_type', 'interactive');
  const skipOption = onboardingExperiment.get('allow_skip', false);

  // Search algorithm experiment
  const searchExperiment = statsigClient.getExperiment('search_algorithm_test');
  const searchAlgorithm = searchExperiment.get('algorithm', 'elasticsearch');
  const fuzzyMatchEnabled = searchExperiment.get('fuzzy_matching', true);
  const maxResults = searchExperiment.get('max_results', 20);

  // Pricing experiment
  const pricingExperiment = statsigClient.getExperiment('pricing_model_experiment');
  const pricingModel = pricingExperiment.get('model', 'tiered');
  const showComparison = pricingExperiment.get('show_comparison_table', true);
  const trialDays = pricingExperiment.get('trial_days', 14);

  // Layer-based experiment
  const promoLayer = statsigClient.getLayer('promotional_campaigns_layer');
  const promoType = promoLayer.get('campaign_type', 'seasonal');
  const promoDiscount = promoLayer.get('discount_percentage', 0.15);
  const promoEndDate = promoLayer.get('end_date', '2024-12-31');
}

// ============================================
// 6. CUSTOM EVENT LOGGING
// ============================================
function logCustomEvents() {
  // Simple event
  statsigClient.logEvent('page_view', 'homepage');

  // Event with value
  statsigClient.logEvent('purchase_completed', 159.99, {
    product_id: 'prod-123',
    product_name: 'Premium Subscription',
    currency: 'USD',
    payment_method: 'credit_card'
  });

  // User action events
  statsigClient.logEvent('button_clicked', null, {
    button_id: 'cta-hero',
    button_text: 'Get Started',
    page: 'landing'
  });

  // Error event
  statsigClient.logEvent('error_occurred', null, {
    error_type: 'validation',
    error_message: 'Email format invalid',
    form_id: 'signup-form'
  });

  // Performance event
  statsigClient.logEvent('api_call_performance', null, {
    endpoint: '/api/users',
    duration_ms: 234,
    status_code: 200,
    cache_hit: false
  });

  // Feature usage event
  statsigClient.logEvent('feature_used', null, {
    feature_name: 'bulk_export',
    items_count: 150,
    format: 'csv'
  });
}

// ============================================
// 7. UTILITY FUNCTIONS (Referenced Above)
// ============================================
function renderNewDashboard() {
  document.getElementById('dashboard-container').innerHTML = '<div>New Dashboard UI</div>';
}

function renderLegacyDashboard() {
  document.getElementById('dashboard-container').innerHTML = '<div>Legacy Dashboard UI</div>';
}

function enablePremiumUI() {
  document.body.classList.add('premium-user');
}

// ============================================
// 8. MAIN EXECUTION
// ============================================
async function main() {
  // Initialize Statsig
  await initializeStatsig();

  // Check all feature gates
  checkFeatureGates();

  // Get all dynamic configs
  getDynamicConfigs();

  // Run experiments
  runExperiments();

  // Log some events
  logCustomEvents();

  // Update user properties
  await statsigClient.updateUserAsync({
    ...statsigUser,
    custom: {
      ...statsigUser.custom,
      lastActivity: new Date().toISOString()
    }
  });

  // Manual exposure logging
  statsigClient.manuallyLogGateExposure('manually_exposed_gate');
  statsigClient.manuallyLogConfigExposure('manually_exposed_config');
  statsigClient.manuallyLogExperimentExposure('manually_exposed_experiment');

  // Shutdown (cleanup)
  window.addEventListener('beforeunload', () => {
    statsigClient.shutdown();
  });
}

// Run the application
main().catch(console.error);

// ============================================
// 9. ADDITIONAL EDGE CASES
// ============================================

// Check if Statsig is ready
if (statsigClient.loadingStatus === 'Ready') {
  console.log('Client is ready');
}

// Get all evaluated values
const allValues = statsigClient.getEvaluatedValues();
console.log('All evaluated values:', allValues);

// Override gate value locally (for testing)
statsigClient.overrideGate('test_gate', true);

// Override config value locally (for testing)
statsigClient.overrideConfig('test_config', { key: 'value' });

// Get stable ID (device identifier)
const stableID = statsigClient.getStableID();
console.log('Stable ID:', stableID);