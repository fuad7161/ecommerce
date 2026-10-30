# Project: Complete E-commerce Platform with Admin Dashboard

Act as a Senior Software Architect, UI/UX Designer, and Full-Stack Engineer.

I want you to design and develop a complete, production-ready e-commerce platform. This is not just a frontend prototype. I need a fully functional system with a customer-facing storefront, an admin dashboard, backend APIs, database integration, order management, product management, and delivery integration.

The platform will initially operate in Bangladesh.

## 1. Core Business Requirements

Build two separate interfaces:

1. **Customer Storefront:** Customers can browse products, search, filter, view offers, add products to their cart, and place orders.
2. **Admin Dashboard:** The store owner can manage products, categories, inventory, offers, orders, customers' delivery information, and business operations.

Both interfaces must use the same backend and database.

### Important Business Rules

- Customer registration is NOT required.
- Customer login is NOT required.
- Customers can browse all publicly available products without authentication.
- Customers can add products to their cart without authentication.
- Customers can place orders using guest checkout.
- Cash on Delivery (COD) is the initial payment method.
- Customers must provide valid contact and delivery information before placing an order.
- Integrate Steadfast Courier for delivery management.
- Admin dashboard must be protected by authentication and authorization.
- Product prices, stock, discounts, and order totals must always be validated on the backend.

## 2. Customer Storefront

Build a modern, responsive, mobile-first e-commerce website.

### A. Homepage

Include:

- Modern navigation bar with logo and search.
- Product categories.
- Hero banners and promotional campaigns.
- Featured products.
- Best-selling products.
- New arrivals.
- Special offers and discounted products.
- Trending products.
- Promotional sections.
- Footer with contact information, policies, and social links.

All homepage sections should be manageable from the admin dashboard.

### B. Product Catalog

Customers should be able to:

- View all active products.
- Browse products by category and subcategory.
- Search products by name, SKU, description, or relevant keywords.
- Filter by category.
- Filter by price range.
- Filter by availability.
- Filter by discount or offer.
- Sort by price (low to high / high to low).
- Sort by newest.
- Sort by popularity.
- Sort by best-selling.
- Use pagination or infinite scrolling.

Search and filtering should work through backend APIs, not just frontend filtering.

### C. Product Details

Each product should support:

- Product name.
- Product slug.
- SKU.
- Multiple product images.
- Product description.
- Product specifications.
- Regular price.
- Selling price.
- Discount information.
- Available stock.
- Category.
- Brand (optional).
- Product variants (size, color, etc.).
- Related products.
- Similar products.
- Offer information.

Customers should be able to select product variants, adjust quantity, and add products to their cart.

Do not allow purchasing quantities exceeding available stock.

### D. Shopping Cart

The shopping cart must work without authentication.

Features:

- Add to cart.
- Remove from cart.
- Increase/decrease quantity.
- Update cart quantities.
- Display product images and prices.
- Display subtotal.
- Display discount.
- Display delivery charge when applicable.
- Display final payable amount.
- Persist cart data using local storage or another appropriate guest-cart mechanism.
- Restore the cart after refreshing the page.
- Handle unavailable or out-of-stock products.
- Revalidate prices and inventory during checkout.

A customer should be able to continue shopping and return to their cart without losing its contents.

### E. Guest Checkout

This is one of the most important features.

Customers must be able to place orders without creating an account.

Create a simple, user-friendly checkout page.

Required customer information:

- Full name.
- Valid mobile number.
- Delivery address.
- District/city.
- Area or thana.
- Optional delivery instructions.

Checkout requirements:

1. Validate the customer's name.
2. Validate the Bangladeshi mobile number.
3. Collect complete delivery address information.
4. Show order items and quantities.
5. Show subtotal, discounts, delivery charge, and total payable amount.
6. Allow customers to select Cash on Delivery.
7. Require explicit confirmation before submitting the order.
8. Create the order using a unique order number.
9. Reduce or reserve inventory safely.
10. Return an order confirmation page.

After placing an order, display:

- Order number.
- Customer name.
- Ordered products.
- Quantities.
- Total payable amount.
- Delivery address.
- Payment method.
- Order status.

Do not expose internal database IDs or sensitive administrative information.

### F. Order Tracking

Customers should be able to track orders without logging in.

Provide an order tracking page requiring:

- Order number.
- Phone number used during checkout.

Display relevant order statuses:

- Pending confirmation.
- Confirmed.
- Processing.
- Ready for shipment.
- Shipped.
- In transit.
- Delivered.
- Cancelled.
- Returned.

Only show information belonging to the matching order and phone number.

## 3. Admin Dashboard

Build a separate, secure admin dashboard.

Use a professional dashboard layout with a sidebar, navigation, tables, filters, charts, and forms.

### A. Dashboard Overview

Display:

- Total revenue.
- Today's revenue.
- Total orders.
- Today's orders.
- Pending orders.
- Processing orders.
- Delivered orders.
- Cancelled orders.
- Total products.
- Low-stock products.
- Best-selling products.
- Recent orders.
- Sales overview chart.
- Order status distribution.

Use actual database information rather than static or dummy statistics.

### B. Product Management

Admin should be able to:

- Create products.
- Edit products.
- Delete or archive products.
- Activate/deactivate products.
- Upload multiple product images.
- Set product name and description.
- Assign categories and subcategories.
- Manage SKU.
- Set regular and selling prices.
- Configure discounts.
- Manage stock quantities.
- Configure product variants.
- Mark products as featured.
- Mark products as best sellers.
- Manage product specifications.
- Preview products before publishing.

Include product listing with:

- Search.
- Category filter.
- Stock filter.
- Status filter.
- Pagination.
- Sorting.

### C. Category Management

Admin can:

- Create categories.
- Edit categories.
- Delete categories.
- Upload category images.
- Manage parent and child categories.
- Set category display order.
- Enable or disable categories.

### D. Offer and Discount Management

Build a complete offer management module.

Admin can create promotional campaigns with:

- Offer title.
- Description.
- Banner image.
- Discount type (percentage or fixed amount).
- Discount value.
- Start date and end date.
- Applicable products.
- Applicable categories.
- Active/inactive status.
- Promotional display settings.

Support automatic product discounts and campaign-based offers.

Validate discount rules on the backend. Prevent expired offers and invalid discount combinations.

### E. Order Management

Provide a complete order management interface.

Admin should be able to:

- View all orders.
- Search by order number or phone number.
- Filter by date.
- Filter by order status.
- Filter by payment method.
- View complete order details.
- View customer delivery information.
- Update order status.
- Add internal notes.
- Cancel orders.
- Process returns.
- Print invoices.
- Export orders to CSV.

Maintain a complete order status history, including who changed the status and when.

### F. Steadfast Courier Integration

Integrate the official Steadfast Courier API.

Admin should be able to:

- Configure Steadfast API credentials securely.
- View delivery eligibility and supported workflows.
- Create a courier delivery request from an order.
- Submit customer name, phone number, delivery address, COD amount, and order reference.
- Receive and store the courier consignment ID.
- Display courier delivery status.
- Track delivery updates.
- Handle API errors and retries.
- Prevent duplicate consignment creation.
- Store courier responses and delivery history.

Important:

- Verify the current official Steadfast API documentation before implementing.
- Keep API credentials in environment variables or secure server-side configuration.
- Never expose API credentials in frontend code.
- Do not automatically submit an order to Steadfast immediately upon checkout unless explicitly configured.
- Allow the admin to confirm an order before creating a courier consignment.
- Implement webhook/status synchronization if supported by the official API.
- Do not invent API endpoints or request fields.

### G. Inventory Management

Implement inventory management with:

- Current stock.
- Reserved stock.
- Available stock.
- Low-stock alerts.
- Stock adjustments.
- Stock history.
- Inventory movement records.
- Automatic stock handling for cancelled orders and returns.

All inventory updates must be transaction-safe.

### H. Customer Information

Since customers do not register, maintain customer information based on their orders.

Admin can:

- View customers.
- Search by phone number.
- View order history associated with a phone number.
- View delivery information.
- See total orders and spending.
- Add internal customer notes.

Do not require a customer account.

### I. Store Settings

Admin should be able to configure:

- Store name and logo.
- Contact information.
- Store address.
- Social media links.
- Delivery charges.
- Delivery areas.
- Free delivery threshold.
- COD availability.
- Homepage banners.
- Basic SEO settings.
- Store policies.

### J. Admin Authentication and Authorization

Implement:

- Secure admin login.
- Logout.
- Password hashing.
- Session management.
- Role-based access control.
- Admin and staff roles.
- Permission-based dashboard access.
- Audit logs for important administrative actions.

Public customer APIs must remain separate from protected admin APIs.

## 4. Backend Architecture

Build a clean, maintainable backend.

Use:

- Modular architecture.
- RESTful APIs.
- Proper request validation.
- Centralized error handling.
- Database transactions.
- Logging.
- Pagination.
- Filtering and sorting.
- API versioning.
- Authentication middleware for admin routes.
- Rate limiting.
- Secure file upload handling.

Suggested modules:

- Authentication
- Users/Admins
- Products
- Categories
- Product Variants
- Inventory
- Cart
- Checkout
- Orders
- Offers
- Customers
- Delivery
- Steadfast Integration
- Store Settings
- Reports
- Audit Logs

Keep business logic out of HTTP handlers/controllers.

## 5. Suggested Database Entities

Design a normalized relational database.

Consider these entities:

- admins
- roles
- permissions
- admin_role_assignments
- categories
- products
- product_images
- product_variants
- inventory
- inventory_movements
- offers
- offer_products
- offer_categories
- orders
- order_items
- order_status_history
- customers
- customer_addresses
- delivery_zones
- delivery_charges
- courier_consignment
- store_settings
- audit_logs

Add appropriate:

- Primary keys.
- Foreign keys.
- Unique constraints.
- Indexes.
- Timestamps.
- Soft deletion where appropriate.

Store order item prices and product information as snapshots so historical orders remain accurate even when products are updated later.

Avoid unnecessarily storing guest cart information in the database unless required.

## 6. Security and Reliability

Implement the following:

- Backend-side price calculation.
- Backend-side stock validation.
- Atomic order creation.
- Inventory race-condition prevention.
- Duplicate order submission protection using idempotency.
- Input validation and sanitization.
- SQL injection protection.
- XSS protection.
- CSRF protection where applicable.
- Rate limiting for checkout and tracking endpoints.
- Secure admin sessions.
- Secure image uploads.
- Environment-based configuration.
- Structured logs.
- Database backup strategy.

Never trust product prices, discounts, delivery fees, or totals submitted by the frontend.

## 7. UI/UX Requirements

The storefront should be modern, clean, and visually attractive.

Design direction:

- Professional e-commerce design.
- Mobile-first responsive layout.
- Clean typography.
- Consistent spacing.
- Attractive product cards.
- High-quality product images.
- Clear pricing and discount labels.
- Easy-to-use search and filters.
- Simple checkout experience.
- Clear loading, success, and error states.
- Accessible forms and controls.

The admin dashboard should prioritize usability and operational efficiency rather than decorative design.

Support desktop, tablet, and mobile screens.

Avoid excessive animations and unnecessary UI complexity.

## 8. SEO and Performance

Implement:

- SEO-friendly product URLs.
- Dynamic page titles and descriptions.
- Product metadata.
- Open Graph metadata.
- Sitemap.
- Robots.txt.
- Optimized images.
- Lazy loading.
- Server-side rendering or an appropriate SEO-friendly rendering strategy.
- Caching for frequently accessed catalog data.
- Database query optimization.

## 9. Testing Requirements

Write tests for critical workflows.

At minimum, test:

1. Product creation and updates.
2. Product search and filtering.
3. Adding products to cart.
4. Cart persistence.
5. Guest checkout.
6. Invalid phone number validation.
7. Order creation.
8. Duplicate order submission.
9. Concurrent inventory updates.
10. Discount calculation.
11. Out-of-stock scenarios.
12. Order cancellation and inventory restoration.
13. Admin authentication and permissions.
14. Steadfast integration and API failures.
15. Customer order tracking.

Include unit tests, integration tests, and end-to-end tests.

## 10. Deployment and Documentation

Provide:

- Dockerfile.
- Docker Compose configuration.
- Database migration scripts.
- Environment variable example file.
- Seed data.
- API documentation.
- Local development instructions.
- Production deployment instructions.
- Database backup instructions.
- Admin account initialization instructions.

Do not hardcode secrets or production credentials.

## 11. Development Workflow

Before writing code:

1. Inspect the existing repository and identify its current structure.
2. Understand the existing technologies and conventions.
3. Propose the architecture.
4. Define the database schema.
5. Define the API contract.
6. Identify external integration requirements.
7. Create an implementation plan.

Then implement the project incrementally.

Recommended development phases:

Phase 1: Project setup, database, architecture, and authentication.

Phase 2: Product catalog, categories, search, and filtering.

Phase 3: Guest cart and checkout.

Phase 4: Order management and inventory.

Phase 5: Admin dashboard and reporting.

Phase 6: Steadfast Courier integration.

Phase 7: SEO, performance, security, and testing.

Phase 8: Deployment and documentation.

After each phase:

- Run relevant tests.
- Fix errors.
- Review implementation quality.
- Update documentation.
- Ensure previously implemented features still work.

## 12. Important Implementation Rules

- Do not build a static UI with fake functionality.
- Do not use hardcoded dashboard statistics.
- Do not leave critical functionality as TODO comments.
- Do not invent Steadfast API specifications.
- Do not expose sensitive information.
- Do not implement customer login or registration.
- Do not trust frontend-calculated order totals.
- Do not skip database migrations or tests.
- Do not unnecessarily over-engineer the architecture.

If something is unclear, make a reasonable engineering decision, document the assumption, and continue.

Prioritize a reliable, maintainable, secure, and production-ready system.

**Final deliverable:** A complete working e-commerce application with a customer storefront, guest checkout, Cash on Delivery, Steadfast Courier integration, and a fully functional admin dashboard.
